package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/pipeline"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/work"
)

// The source timestamp is fixture data, deliberately distinct from the actual
// admission/publication timestamps. This control never fetches current weather.
const completionWeatherSource = `{"source":"recorded acceptance fixture","observed_at":"2024-01-01T12:00:00Z","location":"Denver","conditions":"clear","temperature_f":62,"limitations":"Recorded fixture; not a current network forecast."}`

type completionBoundaryReport struct {
	Mode               string                    `json:"mode"`
	Injection          string                    `json:"injection"`
	SourceSHA256       string                    `json:"source_sha256"`
	SourceObservedAt   string                    `json:"source_observed_at"`
	Published          work.ResultView           `json:"published"`
	Phases             []completionBoundaryPhase `json:"phases"`
	Final              work.Snapshot             `json:"final"`
	GenerationEntered  time.Time                 `json:"generation_entered,omitempty"`
	GenerationCanceled time.Time                 `json:"generation_canceled,omitempty"`
	RetainedInput      bool                      `json:"retained_input"`
	Error              string                    `json:"error,omitempty"`
}

type completionBoundaryPhase struct {
	Name                    string                 `json:"name"`
	Inputs                  []string               `json:"inputs,omitempty"`
	Responses               []events.ResponseReady `json:"responses,omitempty"`
	Metrics                 []events.TurnMetrics   `json:"metrics,omitempty"`
	Deliveries              []events.WorkDelivery  `json:"deliveries,omitempty"`
	Errors                  []events.Error         `json:"errors,omitempty"`
	SpokenSegments          []string               `json:"spoken_segments,omitempty"`
	WeatherFactsVerified    bool                   `json:"weather_facts_verified"`
	SpeakingStarted         int                    `json:"speaking_started"`
	SpeakingComplete        int                    `json:"speaking_complete"`
	SpeakingInterrupted     int                    `json:"speaking_interrupted"`
	ReadSamples             int64                  `json:"read_samples"`
	NonzeroReferenceSamples int                    `json:"nonzero_reference_samples"`
	Capture                 *acousticReport        `json:"capture,omitempty"`
	Work                    work.Snapshot          `json:"work"`
	Error                   string                 `json:"error,omitempty"`
}

// Only the first completion-generation attempt uses this gate. It returns no
// text, touches no provider history, and has no release/revoke API. The real
// retained STT owner must cancel the lease, which cancels this exact context.
// The normal provider is restored before resumed input or successful delivery.
type completionGenerationGate struct {
	brain.Provider
	entered  chan time.Time
	canceled chan time.Time
	err      error // read only after the joined generation call
}

func (g *completionGenerationGate) Complete(ctx context.Context, _ brain.CompletionRequest) (brain.CompletionEnvelope, error) {
	select {
	case g.entered <- time.Now():
	default:
		return brain.CompletionEnvelope{}, errors.New("completion gate entered more than once")
	}
	<-ctx.Done()
	g.err = ctx.Err()
	g.canceled <- time.Now()
	return brain.CompletionEnvelope{}, ctx.Err()
}

func observeCompletionBoundary(bus *events.Bus, name string, run func() error) (completionBoundaryPhase, error) {
	phase := completionBoundaryPhase{Name: name}
	var mu sync.Mutex
	remove := bus.ObserveBeforeFanout(func(e events.Event) {
		mu.Lock()
		defer mu.Unlock()
		switch e := e.(type) {
		case events.UserInput:
			phase.Inputs = append(phase.Inputs, e.Text)
		case events.ResponseReady:
			phase.Responses = append(phase.Responses, e)
		case events.TurnMetrics:
			phase.Metrics = append(phase.Metrics, e)
		case events.WorkDelivery:
			phase.Deliveries = append(phase.Deliveries, e)
		case events.Error:
			phase.Errors = append(phase.Errors, e)
		case events.SpeakingStarted:
			phase.SpeakingStarted++
			phase.SpokenSegments = append(phase.SpokenSegments, e.Text)
		case events.SpeakingComplete:
			phase.SpeakingComplete++
			if e.Interrupted {
				phase.SpeakingInterrupted++
			}
		case events.SpeakingInterrupted:
			phase.SpeakingInterrupted++
		}
	})
	err := run()
	remove()
	mu.Lock()
	defer mu.Unlock()
	if err != nil {
		phase.Error = err.Error()
	}
	return phase, err
}

func validateCompletionBoundaryPhase(p completionBoundaryPhase, kind, expected string) error {
	if p.Error != "" || len(p.Errors) != 0 || p.SpeakingInterrupted != 0 {
		return fmt.Errorf("%s: public error, interrupted playback, or failed operation", p.Name)
	}
	for _, d := range p.Deliveries {
		if d.Stage == "announcement.fallback" || d.Stage == "announcement.held" {
			return fmt.Errorf("%s: unexpected delivery %s (%s)", p.Name, d.Stage, d.Reason)
		}
	}
	if kind == "muted" || kind == "empty" {
		if len(p.Inputs)+len(p.Responses)+len(p.Metrics)+len(p.Deliveries)+p.SpeakingStarted+p.SpeakingComplete != 0 || p.NonzeroReferenceSamples != 0 {
			return fmt.Errorf("%s: silent no-op produced input, generation, output, or receipt", p.Name)
		}
		return nil
	}
	if kind == "revoked" {
		if len(p.Inputs) != 0 || len(p.Responses) != 0 || p.SpeakingStarted != 0 || p.SpeakingComplete != 0 || p.NonzeroReferenceSamples != 0 || len(p.Metrics) != 1 || p.Metrics[0].Outcome != "interrupted" || p.Metrics[0].Degraded {
			return fmt.Errorf("%s: generation did not stop before any response delivery", p.Name)
		}
		scheduled := 0
		for _, d := range p.Deliveries {
			if d.Stage == "announcement.scheduled" && d.AttemptID != "" && d.CaptureEpoch != 0 {
				scheduled++
			}
			if d.Stage == "announcement.communicated" {
				return errors.New("revoked generation communicated result")
			}
		}
		if scheduled != 1 {
			return errors.New("generation was not entered under exactly one capture lease")
		}
		return nil
	}
	if len(p.Metrics) != 1 || p.Metrics[0].Outcome != "completed" || p.Metrics[0].Interrupted || p.Metrics[0].Degraded || p.Metrics[0].TTSFallbacks != 0 || p.Metrics[0].StallWatchdogFired || p.Metrics[0].AudibleSink != string(audio.SinkLocalSpeaker) || len(p.Responses) != 1 || p.Responses[0].Degraded || p.SpeakingStarted == 0 || p.SpeakingStarted != p.SpeakingComplete || p.NonzeroReferenceSamples <= 0 {
		return fmt.Errorf("%s: missing healthy completed voiced turn", p.Name)
	}
	if expected == "" {
		if len(p.Inputs) != 0 {
			return errors.New("completion invented user input")
		}
	} else if len(p.Inputs) != 1 || normalizeAcousticText(p.Inputs[0]) != normalizeAcousticText(expected) {
		return fmt.Errorf("%s: expected one exact user input %q, got %q", p.Name, expected, p.Inputs)
	}
	return nil
}

func requireCompletionPending(s work.Snapshot, result work.ResultView) error {
	if s.Retired || s.Scope != result.Outcome.Scope || len(s.Jobs) != 1 || s.Jobs[0].Key.JobID != result.Outcome.JobID || len(s.Communicated) != 0 || len(s.Pending) != 1 {
		return errors.New("expected exactly the original scoped pending result, without any communication")
	}
	r := s.Pending[0]
	if r.Outcome.ResultID != result.Outcome.ResultID || r.Outcome.TerminalRevision != result.Outcome.TerminalRevision || (r.State != work.Pending && r.State != work.Ready) || r.AnnouncementState == work.AnnouncementCommunicated {
		return errors.New("pending result changed identity or became ineligible")
	}
	return nil
}

func requireCompletionDelivered(s work.Snapshot, result work.ResultView) (string, error) {
	if s.Retired || s.Scope != result.Outcome.Scope || len(s.Jobs) != 0 || len(s.RetiredJobIDs) != 1 || s.RetiredJobIDs[0] != result.Outcome.JobID || len(s.Pending) != 0 || len(s.Communicated) != 1 {
		return "", errors.New("expected exactly one communicated scoped result and no pending result")
	}
	r := s.Communicated[0]
	if r.Outcome.ResultID != result.Outcome.ResultID || r.Outcome.TerminalRevision != result.Outcome.TerminalRevision || r.AnnouncementState != work.AnnouncementCommunicated || r.LastAttempt == nil {
		return "", errors.New("communicated result identity or evidence missing")
	}
	receipt := r.LastAttempt.Receipt
	if receipt.Scope != s.Scope || receipt.AttemptID == "" || receipt.CaptureEpoch == 0 || receipt.Mode != "voice" || !receipt.Audible || !receipt.PlaybackComplete || !receipt.ResponseCommitted || receipt.Interrupted || receipt.Segments <= 0 {
		return "", errors.New("result lacks a scoped, completed audible voice receipt")
	}
	return receipt.AttemptID, nil
}

type completionListenResult struct {
	text string
	err  error
}

// runCompletionBoundary serializes the production host operations. The only
// injected boundary is a recorded result publication (and a generation gate in
// speech mode). Capture, pause, admission/revocation, synthesis, and playback
// are supplied by the same real pipeline used by the ordinary control.
func runCompletionBoundary(ctx context.Context, p *pipeline.Pipeline, mode, expected, dir string, reads func() int64, references func() int, play func(context.Context) (acousticReport, error)) (runErr error) {
	if ctx == nil || p == nil || p.Events == nil || reads == nil || references == nil || play == nil || p.WorkCoordinator() == nil {
		return errors.New("completion boundary: missing pipeline dependencies")
	}
	if mode != "acoustic_completion_mute" && mode != "acoustic_completion_speech" {
		return errors.New("completion boundary: invalid mode")
	}
	if p.OutputMuted() {
		return errors.New("completion boundary requires initially unmuted output")
	}
	if err := boundaryNoWork(p.WorkSnapshot()); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	report := completionBoundaryReport{Mode: mode, Injection: "recorded weather outcome published through coordinator Register/Started/Publish; no worker inference or current external forecast", SourceObservedAt: "2024-01-01T12:00:00Z"}
	sum := sha256.Sum256([]byte(completionWeatherSource))
	report.SourceSHA256 = hex.EncodeToString(sum[:])
	defer func() {
		if !report.Final.Scope.Valid() {
			report.Final = p.WorkSnapshot()
		}
		if runErr != nil {
			report.Error = runErr.Error()
		}
		data, err := json.MarshalIndent(report, "", "  ")
		if err == nil {
			err = os.WriteFile(filepath.Join(dir, "completion-boundary.json"), append(data, '\n'), 0o600)
		}
		runErr = errors.Join(runErr, err)
	}()
	if err := os.WriteFile(filepath.Join(dir, "weather-source.json"), []byte(completionWeatherSource), 0o600); err != nil {
		return err
	}
	c := p.WorkCoordinator()
	scope := c.Scope()
	admittedAt := time.Now()
	key, err := c.Register(scope, work.Admission{Task: "Report the recorded weather fixture", Origin: "convoflow-recorded-fixture", Provider: "fixture", Model: "recorded-weather-v1", Config: map[string]string{"source_sha256": report.SourceSHA256, "source_observed_at": report.SourceObservedAt}})
	if err != nil {
		return err
	}
	if err = c.Started(key); err != nil {
		return err
	}
	report.Published, err = c.Publish(key, work.Outcome{Scope: scope, JobID: key.JobID, StartedAt: admittedAt, EndedAt: time.Now(), Status: work.OutcomeSucceeded, Text: "Recorded fixture: Denver was clear, 62 degrees Fahrenheit.", SourceRef: "recorded Denver weather snapshot", Limitations: "Recorded on January first, twenty twenty-four, at noon UTC; not a current network forecast"})
	if err != nil {
		return err
	}
	var publicErrors []events.Error
	var errorMu sync.Mutex
	removeErrors := p.Events.ObserveBeforeFanout(func(e events.Event) {
		if failure, ok := e.(events.Error); ok {
			errorMu.Lock()
			publicErrors = append(publicErrors, failure)
			errorMu.Unlock()
		}
	})
	defer func() {
		removeErrors()
		errorMu.Lock()
		defer errorMu.Unlock()
		if len(publicErrors) > 0 {
			runErr = errors.Join(runErr, fmt.Errorf("completion boundary public errors: %+v", publicErrors))
		}
	}()
	ctx, cancel := context.WithCancel(ctx)
	var tasks sync.WaitGroup
	original := p.Brain
	defer func() {
		cancel()
		tasks.Wait()
		report.Final = p.WorkSnapshot()
		// Yielded STT sessions outlive their RunTurn. On failure close that owner
		// before the outer capture setup restores or destroys native providers.
		if runErr != nil {
			runErr = errors.Join(runErr, p.CloseWork())
		}
		p.Brain = original
		if p.OutputMuted() {
			p.SetOutputMuted(false)
		}
	}()
	weatherCommunications := 0
	phase := func(name, kind, want string, run func() error) error {
		beforeReads, beforeRefs := reads(), references()
		observation, err := observeCompletionBoundary(p.Events, name, run)
		observation.ReadSamples = reads() - beforeReads
		observation.NonzeroReferenceSamples = references() - beforeRefs
		observation.Work = p.WorkSnapshot()
		if err == nil {
			err = validateCompletionBoundaryPhase(observation, kind, want)
		}
		if err == nil {
			for _, delivery := range observation.Deliveries {
				if delivery.Stage != "announcement.communicated" {
					continue
				}
				for _, id := range delivery.ResultIDs {
					if id != report.Published.Outcome.ResultID {
						continue
					}
					weatherCommunications++
					if weatherCommunications != 1 {
						err = errors.New("weather result communicated more than once")
						break
					}
					err = validateCompletionWeatherSpeech(observation)
					observation.WeatherFactsVerified = err == nil
				}
			}
		}
		if err != nil {
			observation.Error = err.Error()
		}
		report.Phases = append(report.Phases, observation)
		return err
	}
	// Each idle listen starts only after the previous operation has joined.
	listen := func() (chan completionListenResult, error) {
		ready, err := newHardwareReadiness(p.Events)
		if err != nil {
			return nil, err
		}
		defer ready.close()
		done := make(chan completionListenResult, 1)
		tasks.Add(1)
		go func() { defer tasks.Done(); text, e := p.RunTurn(ctx); done <- completionListenResult{text, e} }()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case result := <-done:
			return nil, fmt.Errorf("idle capture resolved before readiness: %q: %w", result.text, errors.Join(result.err, errors.New("early turn")))
		case <-ready.ready:
			return done, nil
		}
	}
	yield := func(done chan completionListenResult) error {
		pauseCtx, stop := context.WithTimeout(ctx, 300*time.Millisecond)
		defer stop()
		acknowledged := false
		var err error
		for !acknowledged && err == nil && pauseCtx.Err() == nil {
			acknowledged, err = p.PauseForCompletion(pauseCtx)
			if !acknowledged && err == nil {
				timer := time.NewTimer(time.Millisecond)
				select {
				case <-pauseCtx.Done():
				case <-timer.C:
				}
				timer.Stop()
			}
		}
		if err != nil || !acknowledged {
			return fmt.Errorf("capture did not yield within 300ms: %w", errors.Join(err, pauseCtx.Err()))
		}
		select {
		case result := <-done:
			if result.err != nil || normalizeAcousticText(result.text) != "" {
				return fmt.Errorf("idle turn did not yield empty: %q: %w", result.text, result.err)
			}
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if mode == "acoustic_completion_mute" {
		p.SetOutputMuted(true)
	}
	idle, err := listen()
	if err != nil {
		return err
	}
	if mode == "acoustic_completion_mute" {
		err = phase("pending-muted", "muted", "", func() error {
			if err := p.RunBackgroundTurn(ctx); err != nil {
				return err
			}
			before := reads()
			timer := time.NewTimer(3 * time.Second)
			defer timer.Stop()
			select {
			case result := <-idle:
				return fmt.Errorf("muted capture ended early: %q %v", result.text, result.err)
			case <-ctx.Done():
				return ctx.Err()
			case <-timer.C:
			}
			if reads()-before < int64(audio.SampleRate*29/10) {
				return errors.New("muted window lacks 2.9 seconds of healthy capture reads")
			}
			if p.HasCapturedInput() {
				return errors.New("muted quiet window admitted unexpected input")
			}
			return requireCompletionPending(p.WorkSnapshot(), report.Published)
		})
		if err != nil {
			return err
		}
		p.SetOutputMuted(false)
	}
	if err = yield(idle); err != nil {
		return err
	}
	if err = requireCompletionPending(p.WorkSnapshot(), report.Published); err != nil {
		return err
	}
	if mode == "acoustic_completion_speech" {
		gate := &completionGenerationGate{Provider: original, entered: make(chan time.Time, 1), canceled: make(chan time.Time, 1)}
		p.Brain = gate
		var played acousticReport
		err = phase("speech-revokes-generation", "revoked", "", func() error {
			done := make(chan error, 1)
			tasks.Add(1)
			go func() { defer tasks.Done(); done <- p.RunBackgroundTurn(ctx) }()
			select {
			case report.GenerationEntered = <-gate.entered:
			case e := <-done:
				return fmt.Errorf("completion ended before gate entry: %v", e)
			case <-ctx.Done():
				return ctx.Err()
			}
			// The complete fixture is delivered even if speech admission revokes the
			// generation early. It remains owned by the paused STT session.
			var e error
			played, e = play(ctx)
			if e == nil {
				e = validateCompletionStimulus(played)
			}
			if e != nil {
				return e
			}
			select {
			case e = <-done:
			case <-ctx.Done():
				return ctx.Err()
			}
			select {
			case report.GenerationCanceled = <-gate.canceled:
			default:
				return errors.New("generation did not observe lease cancellation")
			}
			if ctx.Err() != nil || !errors.Is(e, context.Canceled) || !errors.Is(gate.err, context.Canceled) {
				return fmt.Errorf("generation was not canceled by speech: operation=%v gate=%v outer=%v", e, gate.err, ctx.Err())
			}
			report.RetainedInput = p.HasCapturedInput()
			if !report.RetainedInput {
				return errors.New("generation canceled without retained microphone input")
			}
			return requireCompletionPending(p.WorkSnapshot(), report.Published)
		})
		report.Phases[len(report.Phases)-1].Capture = &played
		if err != nil {
			return err
		}
		p.Brain = original
		err = phase("retained-user-turn", "voice", expected, func() error {
			text, e := p.RunTurn(ctx)
			if e != nil {
				return e
			}
			if normalizeAcousticText(text) != normalizeAcousticText(expected) {
				return fmt.Errorf("retained transcript %q does not match %q", text, expected)
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	if len(p.WorkSnapshot().Pending) > 0 {
		if mode == "acoustic_completion_speech" {
			idle, err = listen()
			if err != nil {
				return err
			}
			if err = yield(idle); err != nil {
				return err
			}
		}
		if err = phase("completed-update", "voice", "", func() error { return p.RunBackgroundTurn(ctx) }); err != nil {
			return err
		}
	}
	receipt, err := requireCompletionDelivered(p.WorkSnapshot(), report.Published)
	if err != nil {
		return err
	}
	// A fresh unrelated fixture must not communicate the result again. In mute
	// mode this also resumes the original yielded session for the first input.
	var played acousticReport
	err = phase("unrelated-no-replay", "voice", expected, func() error {
		text, e := runAcousticTurn(ctx, p.Events, p.RunTurn, func(playCtx context.Context) error {
			var playErr error
			played, playErr = play(playCtx)
			if playErr == nil {
				playErr = validateCompletionStimulus(played)
			}
			return playErr
		})
		if e != nil {
			return e
		}
		if normalizeAcousticText(text) != normalizeAcousticText(expected) {
			return fmt.Errorf("unrelated transcript %q does not match %q", text, expected)
		}
		after, e := requireCompletionDelivered(p.WorkSnapshot(), report.Published)
		if e != nil {
			return e
		}
		if after != receipt {
			return errors.New("unrelated input changed original delivery receipt")
		}
		return nil
	})
	report.Phases[len(report.Phases)-1].Capture = &played
	if err != nil {
		return err
	}
	for _, d := range report.Phases[len(report.Phases)-1].Deliveries {
		if len(d.ResultIDs) > 0 {
			return errors.New("unrelated input projected or replayed an already communicated result")
		}
	}
	if weatherCommunications != 1 {
		return errors.New("weather result lacks one verified public speech communication")
	}
	return phase("drained-completion-no-replay", "empty", "", func() error { return p.RunBackgroundTurn(ctx) })
}

func validateCompletionStimulus(a acousticReport) error {
	if a.Error != "" || a.CaptureReadSamples <= 0 || !a.StimulusWindowValid || !a.StimulusAudible || a.EnvelopeCorrelation < acousticMinEnvelopeCorrelation || a.CapturedPeak >= .999 {
		return errors.New("completion stimulus lacks complete audible aligned PCM and healthy capture reads")
	}
	return nil
}

// Check the finite recorded fixture's facts in both the public response and
// the actual segment text whose playback started. ResponseReady may contain
// text removed by voice projection, so it cannot stand in for spoken content.
// This is a conservative fixture assertion, not general weather fact checking
// or verification that a TTS engine pronounced each word correctly.
func validateCompletionWeatherSpeech(p completionBoundaryPhase) error {
	if len(p.Responses) != 1 || len(p.SpokenSegments) != p.SpeakingStarted || p.SpeakingStarted == 0 || p.SpeakingComplete != p.SpeakingStarted || p.SpeakingInterrupted != 0 {
		return errors.New("weather update lacks a completed response and matching spoken segments")
	}
	for _, item := range []struct{ name, text string }{{"public response", p.Responses[0].Response}, {"spoken segments", strings.Join(p.SpokenSegments, " ")}} {
		text := " " + normalizeAcousticText(item.text) + " "
		has := func(phrase string) bool { return strings.Contains(text, " "+phrase+" ") }
		if !has("denver") || !has("clear") || (!has("62") && !has("sixty 2")) {
			return fmt.Errorf("weather %s omitted or changed Denver, clear, or 62 degrees", item.name)
		}
		if !has("recorded") && !has("snapshot") && !has("historical") && !has("2024") && !has("twenty twenty 4") {
			return fmt.Errorf("weather %s omitted recorded/historical qualification", item.name)
		}
		for _, negated := range []string{"not clear", "isn t clear", "wasn t clear", "not 62", "not sixty 2"} {
			if has(negated) {
				return fmt.Errorf("weather %s contradicted a fixture fact", item.name)
			}
		}
		// Keep the explicit no-freshness disclaimer, but reject presenting this
		// known historical observation as today's/current conditions.
		freshness := strings.NewReplacer(
			" not a current ", " ",
			" not current ", " ",
			" not today ", " ",
			" not currently ", " ",
			" not right now ", " ",
			" isn t current ", " ",
			" isn t a current ", " ",
		).Replace(text)
		for _, current := range []string{" current ", " currently ", " today ", " right now "} {
			if strings.Contains(freshness, current) {
				return fmt.Errorf("weather %s presents recorded data as current", item.name)
			}
		}
	}
	return nil
}
