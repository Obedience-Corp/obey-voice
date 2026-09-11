package pipeline

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

const (
	speechGapWaitingText = "waiting_text"
	speechGapSynthQueue  = "synth_queue"
	speechGapSynthesis   = "synthesis"
	speechGapTransport   = "transport"
	speechGapPlayback    = "playback"
	speechGapUnknown     = "unknown"
	qwenAdviceMinTurns   = 3
	qwenAdviceMinAudio   = 5.0
	qwenAdviceMinRTF     = 1.1
	qwenAdviceMinGap     = 2.0
	speechWindowLimit    = 8
	speechConfigLimit    = 8
	speechActiveConfig   = "\x00active-config"
)

type speechAdviceSample struct {
	rtf, audio, synthGap     float64
	streaming, wholeSentence bool
}
type speechAdviceWindow struct {
	mu           sync.Mutex
	samples      []speechAdviceSample
	advised      map[string]bool
	activeConfig string
}

func (p *Pipeline) warnIfSynthesisCannotKeepUp(m events.TurnMetrics) { p.emitSpeechDiagnostics(m) }

func (p *Pipeline) emitSpeechDiagnostics(m events.TurnMetrics) {
	if len(m.Segments) == 0 {
		return
	}
	var out []events.SpeechDiagnostic
	for _, seg := range m.Segments {
		if seg.GapElapsed <= 0 && seg.Underruns == 0 {
			continue
		}
		cause := seg.GapCause
		if cause == "" {
			cause = speechGapUnknown
		}
		out = append(out, events.SpeechDiagnostic{Symptom: "playback_gap", Cause: cause, Confidence: diagnosticConfidence(cause), Provider: seg.Provider, Scope: "segment", DedupeKey: diagnosticKey(seg.Provider, seg.Tier, seg.Place, seg.WorkerStreaming, cause), Gap: seg.GapElapsed, Message: formatGapDiagnostic(seg, cause)})
	}
	for _, seg := range m.Segments {
		if seg.Provider != "" {
			p.resetActiveSpeechConfig(diagnosticKey(seg.Provider, seg.Tier, seg.Place, seg.WorkerStreaming, ""))
		}
	}
	config, provider, ok := comparableQwenTurn(m)
	if !ok {
		config, provider = qwenTurnIdentity(m)
		if config != "" {
			ex := qwenExclusions(m)
			if m.Outcome != "completed" || m.Interrupted {
				ex = append(ex, "incomplete turn")
			}
			if len(ex) == 0 {
				ex = []string{"mixed or unverified engine configuration"}
			}
			out = append(out, comparisonExcluded(config, provider, ex))
		}
		p.emitSpeechDiagnosticsEvents(out)
		return
	}
	window := p.adviceWindow(config)
	if window == nil {
		p.emitSpeechDiagnosticsEvents(out)
		return
	}
	sample, valid, ex := qwenSample(m)
	if !valid {
		if len(ex) == 0 {
			ex = []string{"missing comparable engine timing or audio"}
		}
		out = append(out, comparisonExcluded(config, provider, ex))
		p.emitSpeechDiagnosticsEvents(out)
		return
	}
	var advice *events.SpeechDiagnostic
	window.mu.Lock()
	window.samples = append(window.samples, sample)
	if len(window.samples) > speechWindowLimit {
		window.samples = window.samples[len(window.samples)-speechWindowLimit:]
	}
	if len(window.samples) >= qwenAdviceMinTurns {
		var audio, gap, weighted float64
		streaming, whole := true, true
		for _, item := range window.samples {
			audio += item.audio
			gap += item.synthGap
			weighted += item.rtf * item.audio
			streaming = streaming && item.streaming
			whole = whole && item.wholeSentence
		}
		if audio >= qwenAdviceMinAudio && weighted/audio > qwenAdviceMinRTF && gap >= qwenAdviceMinGap {
			key := config + ":synthesis"
			if !window.advised[key] {
				window.advised[key] = true
				advice = newSpeechAdvice(config, provider, weighted/audio, gap, len(window.samples), audio, streaming && whole)
			}
		}
	}
	window.mu.Unlock()
	if advice != nil {
		out = append(out, *advice)
	}
	p.emitSpeechDiagnosticsEvents(out)
}

func (p *Pipeline) emitSpeechDiagnosticsEvents(in []events.SpeechDiagnostic) {
	for _, e := range in {
		p.emit(e)
	}
}
func comparisonExcluded(config, provider string, ex []string) events.SpeechDiagnostic {
	ex = uniqueStrings(ex)
	return events.SpeechDiagnostic{Symptom: "speech_comparison_excluded", Cause: speechGapUnknown, Confidence: "observed", Provider: provider, Scope: "turn", DedupeKey: config + ":excluded", Exclusions: ex, Message: "Speech measurement excluded from provider comparison: " + strings.Join(ex, ", ")}
}
func newSpeechAdvice(config, provider string, rtf, gap float64, count int, audio float64, remedy bool) *events.SpeechDiagnostic {
	msg := "Speech generation is falling behind playback; the observation does not identify an available remedy."
	action := ""
	if remedy {
		msg = "Speech generation is falling behind playback. Whole-sentence playback may reduce streaming interruptions, at the cost of delaying first audio until synthesis completes."
		action = "qwen_speech_recovery"
	}
	return &events.SpeechDiagnostic{Symptom: "synthesis_starvation", Cause: speechGapSynthesis, Confidence: "high", Provider: provider, Scope: "session", ActionCode: action, DedupeKey: config + ":synthesis", Message: msg, EngineRTF: rtf, Gap: durationSeconds(gap), SampleCount: count, AudioSeconds: audio}
}

func comparableQwenTurn(m events.TurnMetrics) (config, provider string, ok bool) {
	if m.Interrupted || m.Outcome != "completed" {
		return "", "", false
	}
	var first events.SegmentMetric
	for _, seg := range m.Segments {
		if first.Provider == "" {
			first = seg
		}
		if seg.Provider != first.Provider || seg.Tier != first.Tier || seg.Place != first.Place || seg.WorkerStreaming != first.WorkerStreaming || seg.Fallback {
			return "", "", false
		}
	}
	if first.Provider != "qwen3-tts" || first.Provider == "" {
		return "", "", false
	}
	return diagnosticKey(first.Provider, first.Tier, first.Place, first.WorkerStreaming, ""), first.Provider, true
}
func qwenTurnIdentity(m events.TurnMetrics) (config, provider string) {
	for _, seg := range m.Segments {
		if seg.Provider == "qwen3-tts" {
			return diagnosticKey(seg.Provider, seg.Tier, seg.Place, seg.WorkerStreaming, ""), seg.Provider
		}
	}
	return "", ""
}
func qwenExclusions(m events.TurnMetrics) []string {
	var out []string
	for _, seg := range m.Segments {
		if seg.WarmState != events.WarmStateWarm {
			out = append(out, "cold or unverified warm session")
		}
		if seg.Interrupted {
			out = append(out, "interrupted segment")
		}
		if seg.Fallback {
			out = append(out, "mixed fallback engine")
		}
		if seg.Error != "" {
			out = append(out, "segment error")
		}
		if seg.SynthStartedElapsed <= 0 || seg.SynthEndedElapsed <= 0 || seg.SynthEndedElapsed < seg.SynthStartedElapsed {
			out = append(out, "missing comparable synthesis boundaries")
		}
		out = append(out, seg.Exclusions...)
	}
	return uniqueStrings(out)
}

func qwenSample(m events.TurnMetrics) (speechAdviceSample, bool, []string) {
	var audio, engine, gap float64
	var out []string
	sample := speechAdviceSample{wholeSentence: true}
	var previousEnd time.Duration
	for _, seg := range m.Segments {
		if seg.Provider != "qwen3-tts" {
			out = append(out, "mixed or unverified engine configuration")
		}
		if seg.AudioSeconds <= 0 || seg.EngineElapsed <= 0 {
			out = append(out, "missing comparable engine timing or audio")
		}
		if seg.Interrupted || seg.Fallback {
			out = append(out, "interrupted or fallback segment")
		}
		if seg.WarmState != events.WarmStateWarm {
			out = append(out, "cold or unverified warm session")
		}
		if seg.Error != "" {
			out = append(out, "segment error")
		}
		audio += seg.AudioSeconds
		engine += seg.EngineElapsed.Seconds()
		sample.streaming = sample.streaming || seg.WorkerStreaming
		sample.wholeSentence = sample.wholeSentence && seg.WholeSentencePlayback
		for _, g := range seg.Gaps {
			if g.EndElapsed <= g.StartElapsed {
				out = append(out, "invalid gap timing")
				continue
			}
			if g.StartElapsed < previousEnd {
				out = append(out, "overlapping gap timing")
				continue
			}
			previousEnd = g.EndElapsed
			if g.Cause == speechGapSynthesis {
				gap += (g.EndElapsed - g.StartElapsed).Seconds()
			}
		}
	}
	if audio <= 0 || engine <= 0 {
		out = append(out, "missing comparable engine timing or audio")
	}
	if len(out) > 0 {
		return speechAdviceSample{}, false, uniqueStrings(out)
	}
	sample.rtf = engine / audio
	sample.audio = audio
	sample.synthGap = gap
	return sample, true, nil
}

func (p *Pipeline) adviceWindow(config string) *speechAdviceWindow {
	p.speechAdviceMu.Lock()
	defer p.speechAdviceMu.Unlock()
	if p.speechAdvice == nil {
		p.speechAdvice = make(map[string]*speechAdviceWindow)
	}
	active := p.speechAdvice[speechActiveConfig]
	if active == nil {
		active = &speechAdviceWindow{activeConfig: config}
		p.speechAdvice[speechActiveConfig] = active
	}
	if w := p.speechAdvice[config]; w != nil {
		return w
	}
	if len(p.speechAdvice)-1 >= speechConfigLimit {
		return nil
	}
	w := &speechAdviceWindow{advised: make(map[string]bool)}
	p.speechAdvice[config] = w
	return w
}

func (p *Pipeline) resetActiveSpeechConfig(config string) {
	p.speechAdviceMu.Lock()
	defer p.speechAdviceMu.Unlock()
	if p.speechAdvice == nil {
		p.speechAdvice = make(map[string]*speechAdviceWindow)
	}
	active := p.speechAdvice[speechActiveConfig]
	if active == nil {
		p.speechAdvice[speechActiveConfig] = &speechAdviceWindow{activeConfig: config}
		return
	}
	if active.activeConfig == config {
		return
	}
	for key, window := range p.speechAdvice {
		if key == speechActiveConfig {
			continue
		}
		window.mu.Lock()
		window.samples = nil
		window.mu.Unlock()
	}
	active.activeConfig = config
}
func diagnosticKey(provider, tier, place string, streaming bool, cause string) string {
	return fmt.Sprintf("%s|%s|%s|stream=%t|%s", provider, tier, place, streaming, cause)
}
func diagnosticConfidence(cause string) string {
	if cause == speechGapUnknown {
		return "unknown"
	}
	return "observed"
}
func formatGapDiagnostic(seg events.SegmentMetric, cause string) string {
	if seg.GapElapsed > 0 {
		return fmt.Sprintf("Playback gap %.1fs · %s", seg.GapElapsed.Seconds(), cause)
	}
	return fmt.Sprintf("Playback underrun · cause %s", cause)
}
func durationSeconds(seconds float64) time.Duration {
	return time.Duration(seconds * float64(time.Second))
}
func uniqueStrings(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, v := range in {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}
