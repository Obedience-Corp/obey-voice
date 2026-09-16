package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio/playback"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/Obedience-Corp/obey-voice/internal/aecprobe"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/stt"
)

type acousticReport struct {
	CaptureReadSamples      int64                  `json:"capture_read_samples,omitempty"`
	NonzeroReferenceSamples int                    `json:"nonzero_reference_samples,omitempty"`
	Stimulus                string                 `json:"stimulus"`
	ExpectedTranscript      string                 `json:"expected_transcript,omitempty"`
	Transcript              string                 `json:"transcript"`
	InputDevice             string                 `json:"input_device"`
	OutputDevice            string                 `json:"output_device"`
	StimulusDevice          string                 `json:"stimulus_device"`
	NegotiatedCapture       audio.NegotiatedDevice `json:"negotiated_capture"`
	NegotiatedResponse      audio.NegotiatedDevice `json:"negotiated_response"`
	NegotiatedStimulus      audio.NegotiatedDevice `json:"negotiated_stimulus"`
	StimulusRate            int                    `json:"stimulus_rate"`
	CapturedSamples         int                    `json:"captured_samples"`
	CapturedRMS             float64                `json:"captured_rms"`
	CapturedPeak            float64                `json:"captured_peak"`
	StimulusWindowStart     int                    `json:"stimulus_window_start_sample"`
	StimulusWindowEnd       int                    `json:"stimulus_window_end_sample"`
	StimulusWindowRMS       float64                `json:"stimulus_window_rms"`
	StimulusWindowValid     bool                   `json:"stimulus_window_valid"`
	StimulusAudible         bool                   `json:"stimulus_audible"`
	DelaySamples            int                    `json:"delay_samples"`
	DelayMS                 float64                `json:"delay_ms"`
	Correlation             float64                `json:"correlation_confidence"`
	EnvelopeCorrelation     float64                `json:"envelope_correlation"`
	Error                   string                 `json:"error,omitempty"`
}

const (
	// Envelope correlation verifies the acoustic signal path; the exact
	// normalized transcript establishes fixture identity. Different short
	// utterances can have similar envelopes. Calibrated physical recordings
	// on this device scored .68-.88 for the matching stimulus.
	acousticMinEnvelopeCorrelation = 0.6
)

func stimulusOutputDevice(cfg *config.Config, override string) string {
	if device := strings.TrimSpace(override); device != "" {
		return device
	}
	if cfg == nil {
		return ""
	}
	return cfg.OutputDevice
}

// runAcoustic feeds a known speech recording through real speakers and lets
// the real microphone/STT path recognize the acoustic result. The stimulus
// player is intentionally separate from the pipeline player and has no AEC
// frontend, so the user signal is never presented to the echo canceller as a
// far-end response reference.
func (r *liveRunner) runAcoustic(ctx context.Context, path, expected, artifactDir string) error {
	return r.runAcousticCapture(ctx, path, expected, artifactDir, false)
}

// strict wires all live pipeline dependencies to one healthy capture owner and
// retains ordered response references for boundary acceptance.
func (r *liveRunner) runAcousticCapture(ctx context.Context, path, expected, artifactDir string, strict bool) (runErr error) {
	if ctx == nil {
		return errors.New("acoustic run: nil context")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if r == nil || r.agent == nil || r.agent.Pipeline == nil {
		return errors.New("acoustic run: agent is not configured")
	}
	if r.cfg == nil {
		return errors.New("acoustic run: config is not configured")
	}
	if strings.TrimSpace(path) == "" {
		return errors.New("acoustic run: stimulus path is empty")
	}
	if strings.TrimSpace(artifactDir) == "" {
		return errors.New("acoustic run: artifact directory is empty")
	}
	samples, rate, err := audio.ReadWAVFloat32(path)
	if err != nil {
		return fmt.Errorf("acoustic stimulus %q: %w", path, err)
	}
	if len(samples) == 0 || rate <= 0 {
		return fmt.Errorf("acoustic stimulus %q: empty audio", path)
	}
	correlationSamples := samples
	if rate != audio.SampleRate {
		resampled, err := audio.NewFixtureSourceFromWAV(path, audio.ChunkSize, false)
		if err != nil {
			return fmt.Errorf("acoustic stimulus %q: correlation source: %w", path, err)
		}
		correlationSamples = nil
		for chunk := resampled.Read(); len(chunk) > 0; chunk = resampled.Read() {
			correlationSamples = append(correlationSamples, chunk...)
		}
		_ = resampled.Close()
	}
	if err := os.MkdirAll(artifactDir, 0o700); err != nil {
		return fmt.Errorf("acoustic artifacts: %w", err)
	}
	var rawFrontend *rawCaptureFrontend
	var frames *responseCaptureSource
	var capturedMu sync.Mutex
	var captured []float32
	transcript := ""
	var capture *audio.Capture
	var player, response *playback.Player
	stimulusDevice := stimulusOutputDevice(r.cfg, r.stimulusDevice)
	report := acousticReport{Stimulus: path, ExpectedTranscript: strings.TrimSpace(expected), InputDevice: acousticDeviceLabel(r.cfg.InputDevice), OutputDevice: acousticDeviceLabel(r.cfg.OutputDevice), StimulusDevice: acousticDeviceLabel(stimulusDevice), StimulusRate: rate}
	defer func() {
		capturedMu.Lock()
		mic := append([]float32(nil), captured...)
		capturedMu.Unlock()
		processed := mic
		if rawFrontend != nil {
			mic = rawFrontend.RawSnapshot()
		}
		report.Transcript = transcript
		if frames != nil {
			report.CaptureReadSamples = frames.readSamples.Load()
		}
		if strict && rawFrontend != nil {
			report.NonzeroReferenceSamples = nonzeroResponseReferenceSamples(rawFrontend)
		}
		report.CapturedSamples = len(mic)
		report.CapturedRMS = aecprobe.SegmentRMS(mic)
		report.CapturedPeak = aecprobe.PeakLevel(mic)
		if runErr != nil {
			report.Error = runErr.Error()
		}
		if capture != nil {
			report.NegotiatedCapture = capture.NegotiatedDevice()
		}
		if response != nil {
			report.NegotiatedResponse = response.NegotiatedDevice()
		}
		if player != nil {
			report.NegotiatedStimulus = player.NegotiatedDevice()
		}
		var artifactErrs []error
		if strict && rawFrontend != nil {
			artifactErrs = append(artifactErrs, rawFrontend.WriteTrace(filepath.Join(artifactDir, "frontend.jsonl")))
		}
		if err := audio.WriteWAVFloat32(filepath.Join(artifactDir, "stimulus.wav"), rate, samples); err != nil {
			artifactErrs = append(artifactErrs, fmt.Errorf("write acoustic stimulus: %w", err))
		}
		if len(processed) > 0 {
			if err := audio.WriteWAVFloat32(filepath.Join(artifactDir, "mic-processed.wav"), audio.SampleRate, processed); err != nil {
				artifactErrs = append(artifactErrs, err)
			}
		}
		if len(mic) > 0 {
			if err := audio.WriteWAVFloat32(filepath.Join(artifactDir, "mic-in.wav"), audio.SampleRate, mic); err != nil {
				artifactErrs = append(artifactErrs, fmt.Errorf("write acoustic microphone recording: %w", err))
			}
		}
		if data, err := json.MarshalIndent(report, "", "  "); err == nil {
			if err := os.WriteFile(filepath.Join(artifactDir, "metrics.json"), append(data, '\n'), 0o600); err != nil {
				artifactErrs = append(artifactErrs, fmt.Errorf("write acoustic metrics: %w", err))
			}
		} else {
			artifactErrs = append(artifactErrs, fmt.Errorf("encode acoustic metrics: %w", err))
		}
		if len(artifactErrs) > 0 {
			runErr = errors.Join(runErr, errors.Join(artifactErrs...))
		}
	}()

	capture = audio.NewCaptureWithDevice(r.cfg.InputDevice)
	player = playback.NewPlayerWithDevice(stimulusDevice)
	defer capture.Stop()
	defer player.Close()

	vad, err := audio.NewVAD(r.cfg)
	if err != nil {
		return fmt.Errorf("acoustic VAD: %w", err)
	}
	defer vad.Delete()
	var frameSource audio.FrameSource = capture
	if strict {
		frames = &responseCaptureSource{FrameSource: capture, gain: capture}
		frameSource = frames
	}
	endable := audio.NewFinalizable(frameSource)
	recognizer, recognizerCleanup, err := stt.NewProvider(r.cfg, endable, vad)
	if err != nil {
		return fmt.Errorf("acoustic STT: %w", err)
	}
	if recognizerCleanup != nil {
		defer recognizerCleanup()
	}
	frontend, err := interruptionFrontend(r.cfg)
	if err != nil {
		return err
	}
	defer frontend.Close()
	rawFrontend = newRawCaptureFrontend(frontend)
	rawFrontend.EnableTrace(strict)
	capture.SetFrontend(rawFrontend)
	if r.player == nil {
		return errors.New("acoustic run: physical response player missing")
	}
	real, ok := r.player.real.(*playback.Player)
	if !ok {
		return errors.New("acoustic run: physical response player required")
	}
	response = real
	report.NegotiatedCapture = capture.NegotiatedDevice()
	report.NegotiatedResponse = response.NegotiatedDevice()
	report.NegotiatedStimulus = player.NegotiatedDevice()
	if strict {
		real.SetFrontend(rawFrontend)
	} else {
		real.SetFrontend(frontend)
	}
	defer real.SetFrontend(nil)
	streamID, stream := capture.Subscribe(512)
	collectorDone := make(chan struct{})
	go func() {
		defer close(collectorDone)
		for chunk := range stream {
			capturedMu.Lock()
			captured = append(captured, chunk...)
			capturedMu.Unlock()
		}
	}()
	finishCapture := sync.OnceFunc(func() {
		capture.Stop()
		capture.Unsubscribe(streamID)
		<-collectorDone
	})
	defer finishCapture()
	if err := capture.Start(ctx); err != nil {
		return fmt.Errorf("acoustic capture: %w", err)
	}

	p := r.agent.Pipeline
	oldSTT, oldVAD, oldUtterance := p.STT, p.VAD, p.Utterance
	oldCapture, oldBarge := p.Capture, p.BargeInVAD
	if strict {
		barge, err := audio.NewBargeInVAD(r.cfg)
		if err != nil {
			return err
		}
		defer barge.Delete()
		p.Capture, p.BargeInVAD = capture, barge
		defer func() { p.Capture, p.BargeInVAD = oldCapture, oldBarge }()
	}
	p.STT, p.VAD, p.Utterance = recognizer, vad, endable
	defer func() { p.STT, p.VAD, p.Utterance = oldSTT, oldVAD, oldUtterance }()

	text, turnErr := runAcousticTurn(ctx, p.Events, p.RunTurn, func(playCtx context.Context) error {
		return playAcousticStimulus(playCtx, player, samples, rate)
	})
	transcript = strings.TrimSpace(text)
	// Playback completion precedes arrival of the final acoustic/device tail.
	// Freeze the collector before scoring so metrics and retained WAV describe
	// exactly the same samples, including the reference recording's tail.
	tail := time.NewTimer(400 * time.Millisecond)
	defer tail.Stop()
	select {
	case <-tail.C:
	case <-ctx.Done():
		return ctx.Err()
	}
	finishCapture()
	mic := rawFrontend.RawSnapshot()
	scoreErr := scoreAcousticCapture(&report, correlationSamples, mic)
	if turnErr != nil {
		return turnErr
	}
	if transcript == "" {
		return errors.New("acoustic turn produced no transcript")
	}
	if expected = strings.TrimSpace(expected); expected != "" && normalizeAcousticText(transcript) != normalizeAcousticText(expected) {
		return fmt.Errorf("acoustic transcript %q does not match expected %q", transcript, expected)
	}

	return scoreErr
}

// scoreAcousticCapture keeps whole-recording telemetry, but tests audibility
// only over the complete fixture window, using STT's existing microphone
// audibility semantics instead of the legacy .005 whole-capture RMS gate.
// Model wait time and subsequent assistant playback are outside that window. Bounds are capture-rate sample
// indices; the end is exclusive. Alignment retains the existing 10ms envelope
// search and its maximum two-second starting offset.
func scoreAcousticCapture(report *acousticReport, stimulus, mic []float32) error {
	report.CapturedSamples = len(mic)
	report.CapturedRMS = aecprobe.SegmentRMS(mic)
	report.CapturedPeak = aecprobe.PeakLevel(mic)
	if len(mic) < audio.SampleRate/4 {
		return fmt.Errorf("acoustic capture too short: %d samples", len(mic))
	}
	lag, rawConfidence := aecprobe.MeasureDelay(stimulus, mic, audio.SampleRate*2)
	report.DelaySamples = lag
	report.DelayMS = float64(lag) * 1000 / float64(audio.SampleRate)
	report.Correlation = rawConfidence
	start, score := acousticEnvelopeAlignment(stimulus, mic, audio.SampleRate/100, 200)
	report.EnvelopeCorrelation = score
	if start < 0 {
		return errors.New("acoustic stimulus window could not be located")
	}
	report.StimulusWindowStart = start
	report.StimulusWindowEnd = start + len(stimulus)
	if report.StimulusWindowEnd > len(mic) {
		return fmt.Errorf("acoustic stimulus window incomplete: end %d exceeds capture %d", report.StimulusWindowEnd, len(mic))
	}
	if report.EnvelopeCorrelation < acousticMinEnvelopeCorrelation {
		return fmt.Errorf("acoustic stimulus envelope correlation too weak: %.3f", report.EnvelopeCorrelation)
	}
	report.StimulusWindowValid = true
	window := mic[start:report.StimulusWindowEnd]
	report.StimulusWindowRMS = aecprobe.SegmentRMS(window)
	var audibility stt.SpeechAudibility
	// Raw capture has no frontend gain. A completed audible 100ms window
	// survives pauses; the wrapper also preserves STT's trailing-window check.
	report.StimulusAudible = audibility.Observe(window, 1)
	if !report.StimulusAudible {
		return fmt.Errorf("acoustic stimulus window is not audible: rms %.4f", report.StimulusWindowRMS)
	}
	if report.CapturedPeak >= 0.999 {
		return fmt.Errorf("acoustic capture clipped: peak %.4f", report.CapturedPeak)
	}
	return nil
}

// runAcousticTurn subscribes before starting the real pipeline, and permits
// speaker playback only after that pipeline forwards STT's listening phase.
// Every return cancels and joins the turn before its caller restores providers.
func runAcousticTurn(ctx context.Context, bus *events.Bus, runTurn func(context.Context) (string, error), play func(context.Context) error) (string, error) {
	ready, err := newHardwareReadiness(bus)
	if err != nil {
		return "", err
	}
	defer ready.close()
	runCtx, cancel := context.WithCancel(ctx)
	type result struct {
		text string
		err  error
	}
	done := make(chan result, 1)
	joined := make(chan struct{})
	go func() {
		defer close(joined)
		text, err := runTurn(runCtx)
		done <- result{text, err}
	}()
	defer func() { cancel(); <-joined }()
	endedBeforePlayback := func(turn result) (string, error) {
		return turn.text, fmt.Errorf("acoustic turn ended before stimulus playback: %w", errors.Join(errors.New("STT did not remain listening"), turn.err))
	}
	select {
	case <-runCtx.Done():
		return "", runCtx.Err()
	case turn := <-done:
		return endedBeforePlayback(turn)
	case <-ready.ready:
	}
	// A session can report listening then immediately fail. Do not play a
	// fixture when its completed turn is already waiting at this boundary.
	select {
	case <-runCtx.Done():
		return "", runCtx.Err()
	case turn := <-done:
		return endedBeforePlayback(turn)
	default:
	}
	if err := play(runCtx); err != nil {
		return "", fmt.Errorf("acoustic stimulus playback: %w", err)
	}
	select {
	case <-runCtx.Done():
		return "", runCtx.Err()
	case turn := <-done:
		if turn.err != nil {
			return turn.text, fmt.Errorf("acoustic turn: %w", turn.err)
		}
		return turn.text, nil
	}
}

// acousticEnvelopeCorrelation compares 10 ms RMS envelopes over the complete
// stimulus window while allowing up to maxLagFrames of acoustic delay. It is
// deliberately separate from raw waveform correlation, which is sensitive to
// room reflections and independent playback/capture clocks.
func acousticEnvelopeCorrelation(stimulus, mic []float32, frameSize, maxLagFrames int) float64 {
	_, score := acousticEnvelopeAlignment(stimulus, mic, frameSize, maxLagFrames)
	return score
}

func acousticEnvelopeAlignment(stimulus, mic []float32, frameSize, maxLagFrames int) (int, float64) {
	if frameSize <= 0 || len(stimulus) < frameSize || len(mic) < frameSize {
		return -1, 0
	}
	stimulusEnv := rmsEnvelope(stimulus, frameSize)
	micEnv := rmsEnvelope(mic, frameSize)
	best := 0.0
	start := -1
	for lag := 0; lag <= maxLagFrames && lag+len(stimulusEnv) <= len(micEnv); lag++ {
		if score := pearson(stimulusEnv, micEnv[lag:lag+len(stimulusEnv)]); score > best {
			best = score
			start = lag * frameSize
		}
	}
	return start, best
}

func rmsEnvelope(samples []float32, frameSize int) []float64 {
	n := len(samples) / frameSize
	out := make([]float64, n)
	for i := range n {
		var sum float64
		for _, sample := range samples[i*frameSize : (i+1)*frameSize] {
			sum += float64(sample) * float64(sample)
		}
		out[i] = math.Sqrt(sum / float64(frameSize))
	}
	return out
}

func pearson(a, b []float64) float64 {
	if len(a) != len(b) || len(a) < 2 {
		return 0
	}
	var ma, mb float64
	for i := range a {
		ma += a[i]
		mb += b[i]
	}
	ma /= float64(len(a))
	mb /= float64(len(b))
	var num, aa, bb float64
	for i := range a {
		x, y := a[i]-ma, b[i]-mb
		num += x * y
		aa += x * x
		bb += y * y
	}
	if aa == 0 || bb == 0 {
		return 0
	}
	return num / (math.Sqrt(aa) * math.Sqrt(bb))
}

func normalizeAcousticText(text string) string {
	var b strings.Builder
	lastSpace := false
	for _, r := range strings.ToLower(text) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			if lastSpace {
				b.WriteByte(' ')
			}
			b.WriteRune(r)
			lastSpace = false
		} else if b.Len() > 0 {
			lastSpace = true
		}
	}
	words := strings.Fields(b.String())
	// STT may render the same spoken number as a numeral or an English word.
	// Preserve every other word, including negation and units.
	numbers := map[string]string{"zero": "0", "one": "1", "two": "2", "three": "3", "four": "4", "five": "5", "six": "6", "seven": "7", "eight": "8", "nine": "9", "ten": "10"}
	for i, word := range words {
		if numeral, ok := numbers[word]; ok {
			words[i] = numeral
		}
	}
	return strings.Join(words, " ")
}

func acousticDeviceLabel(name string) string {
	if strings.TrimSpace(name) == "" {
		return "(system default)"
	}
	return name
}

func playAcousticStimulus(ctx context.Context, player *playback.Player, samples []float32, rate int) error {
	return playAcousticStimulusObserved(ctx, player, samples, rate, nil)
}

func playAcousticStimulusObserved(ctx context.Context, player *playback.Player, samples []float32, rate int, started func(time.Time)) error {
	stream := audio.NewPCMStream(ctx)
	if err := stream.SetSampleRate(rate); err != nil {
		return err
	}
	go func() {
		for start := 0; start < len(samples); start += audio.ChunkSize {
			end := min(start+audio.ChunkSize, len(samples))
			if err := stream.Write(samples[start:end]); err != nil {
				stream.CloseWithError(err)
				return
			}
		}
		stream.Close()
	}()
	playback, err := player.PlayStream(ctx, stream)
	if err != nil {
		return err
	}
	if started != nil {
		select {
		case <-playback.Started():
			started(time.Now())
		case result := <-playback.Done():
			return errors.Join(errors.New("acoustic stimulus ended before observed playback start"), result.Err)
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	result := <-playback.Done()
	return result.Err
}
