package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio/playback"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/work"
)

const acousticProviderFailure = "acoustic acceptance: injected provider transport failure"

// Injection occurs at the production ThinkStream boundary, after real STT.
// Other operations retain the configured provider; the wrapper is removed
// before the recovery turn. This is a labelled dependency failure, not a claim
// that the operator's remote service was unavailable.
type boundaryFailureProvider struct {
	brain.Provider
	calls atomic.Int32
}

func (p *boundaryFailureProvider) ThinkStream(ctx context.Context, _ string, _ brain.StreamOptions) (*brain.Stream, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	p.calls.Add(1)
	return nil, errors.New(acousticProviderFailure)
}

type boundaryPhaseReport struct {
	Name                     string                 `json:"name"`
	Started                  time.Time              `json:"started"`
	Ended                    time.Time              `json:"ended"`
	Inputs                   []string               `json:"inputs"`
	Responses                []events.ResponseReady `json:"responses"`
	Metrics                  []events.TurnMetrics   `json:"metrics"`
	Errors                   []events.Error         `json:"errors"`
	PlaybackStarts           int                    `json:"playback_starts"`
	PlaybackCompletions      int                    `json:"playback_completions"`
	Acoustic                 acousticReport         `json:"acoustic"`
	WorkBefore               work.Snapshot          `json:"work_before"`
	WorkAfter                work.Snapshot          `json:"work_after"`
	AudibleDeliveryCompleted bool                   `json:"audible_delivery_completed"`
	Error                    string                 `json:"error,omitempty"`
}

type boundaryReport struct {
	Operation        string                `json:"operation"`
	Injection        string                `json:"injection,omitempty"`
	ProviderFailures int32                 `json:"provider_failures"`
	Phases           []boundaryPhaseReport `json:"phases"`
	Passed           bool                  `json:"passed"`
	Error            string                `json:"error,omitempty"`
}

func (r *liveRunner) runAcousticBoundary(ctx context.Context, op, wav, expected, dir string) (runErr error) {
	if ctx == nil || r == nil || r.agent == nil || r.agent.Pipeline == nil || r.cfg == nil || r.player == nil || (op != "acoustic_mute" && op != "acoustic_failure") || wav == "" || normalizeAcousticText(expected) == "" || dir == "" {
		return errors.New("acoustic boundary: invalid dependencies or scenario")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	p := r.agent.Pipeline
	if p.Events == nil || p.Brain == nil {
		return errors.New("acoustic boundary: event bus and provider required")
	}
	response, ok := r.player.real.(*playback.Player)
	if !ok {
		return errors.New("acoustic boundary: real selected response player required")
	}
	if err := requireEchoResponseVolume(response); err != nil {
		return err
	}
	if p.OutputMuted() {
		return errors.New("acoustic boundary: fresh initially unmuted pipeline required")
	}
	if err := boundaryNoWork(r.workSnapshot()); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0700); err != nil {
		return err
	}
	if err := os.Mkdir(dir, 0700); err != nil {
		return fmt.Errorf("acoustic boundary: new artifact directory: %w", err)
	}
	report := boundaryReport{Operation: op}
	original := p.Brain
	failure := &boundaryFailureProvider{Provider: original}
	defer func() {
		p.Brain = original
		p.SetOutputMuted(false)
		report.ProviderFailures = failure.calls.Load()
		if runErr != nil {
			report.Error = runErr.Error()
		}
		data, err := json.MarshalIndent(report, "", "  ")
		if err == nil {
			err = os.WriteFile(filepath.Join(dir, "boundary.json"), append(data, '\n'), 0600)
		}
		runErr = errors.Join(runErr, err)
	}()
	first := "muted"
	if op == "acoustic_failure" {
		first = "failure"
		report.Injection = "ThinkStream returned a controlled transport error after real microphone STT; no remote failure claimed"
		p.Brain = failure
	} else {
		p.SetOutputMuted(true)
	}
	for _, phase := range []string{first, "recovered"} {
		if phase == "recovered" {
			p.Brain = original
			p.SetOutputMuted(false)
		}
		phaseDir := filepath.Join(dir, phase)
		before := r.workSnapshot()
		result, err := observeBoundaryPhase(ctx, p.Events, phase, expected, func(child context.Context) error { return r.runAcousticCapture(child, wav, expected, phaseDir, true) }, func() (acousticReport, error) {
			var a acousticReport
			b, e := os.ReadFile(filepath.Join(phaseDir, "metrics.json"))
			if e != nil {
				return a, e
			}
			e = json.Unmarshal(b, &a)
			return a, e
		})
		result.WorkBefore, result.WorkAfter = before, r.workSnapshot()
		err = errors.Join(err, boundaryNoWork(result.WorkBefore), boundaryNoWork(result.WorkAfter))
		if err != nil {
			result.Error = err.Error()
		}
		report.Phases = append(report.Phases, result)
		if err != nil {
			return err
		}
		if phase == "failure" && failure.calls.Load() != 1 {
			return fmt.Errorf("acoustic boundary: failure injection count=%d want1", failure.calls.Load())
		}
	}
	report.Passed = true
	return nil
}

// These controls intentionally contain ordinary turns, not pending work. An
// unexpected job/result/receipt invalidates them rather than letting an unrelated
// completion masquerade as the fresh recovered response.
func boundaryNoWork(s work.Snapshot) error {
	if len(s.Jobs) != 0 || len(s.Pending) != 0 || len(s.Communicated) != 0 {
		return errors.New("acoustic boundary: unexpected work or delivery receipt")
	}
	return nil
}

// The runner owns cancellation and joins its STT/stimulus before returning.
// This observer only records public facts, and remains attached through capture
// shutdown so late/duplicate events cannot escape validation.
func observeBoundaryPhase(ctx context.Context, bus *events.Bus, phase, expected string, run func(context.Context) error, capture func() (acousticReport, error)) (boundaryPhaseReport, error) {
	report := boundaryPhaseReport{Name: phase, Started: time.Now()}
	if ctx == nil || bus == nil || run == nil || capture == nil {
		return report, errors.New("acoustic boundary: missing phase dependencies")
	}
	var mu sync.Mutex
	var observedErr error
	remove := bus.ObserveBeforeFanout(func(event events.Event) {
		mu.Lock()
		defer mu.Unlock()
		switch e := event.(type) {
		case events.UserInput:
			report.Inputs = append(report.Inputs, e.Text)
		case events.ResponseReady:
			report.Responses = append(report.Responses, e)
		case events.TurnMetrics:
			report.Metrics = append(report.Metrics, e)
		case events.Error:
			report.Errors = append(report.Errors, e)
		case events.SpeakingStarted:
			report.PlaybackStarts++
			if e.AudibleSink != "local_speaker" {
				observedErr = errors.Join(observedErr, errors.New("nonphysical playback start"))
			}
		case events.SpeakingComplete:
			report.PlaybackCompletions++
			if e.AudibleSink != "local_speaker" || e.Interrupted || e.Elapsed <= 0 {
				observedErr = errors.Join(observedErr, errors.New("unsuccessful playback completion"))
			}
		case events.SpeakingInterrupted:
			observedErr = errors.Join(observedErr, fmt.Errorf("unexpected interruption: %s", e.Reason))
		}
	})
	err := run(ctx)
	remove()
	mu.Lock()
	defer mu.Unlock()
	report.Ended = time.Now()
	acoustic, captureErr := capture()
	report.Acoustic = acoustic
	err = errors.Join(err, ctx.Err(), captureErr, observedErr)
	if err == nil {
		err = validateBoundaryPhase(&report, expected)
	}
	if err != nil {
		report.Error = err.Error()
	}
	return report, err
}

func validateBoundaryPhase(r *boundaryPhaseReport, expected string) error {
	if len(r.Inputs) != 1 || normalizeAcousticText(r.Inputs[0]) != normalizeAcousticText(expected) || normalizeAcousticText(r.Acoustic.Transcript) != normalizeAcousticText(expected) {
		return errors.New("acoustic boundary: missing, duplicate or incorrect input")
	}
	a := r.Acoustic
	if a.Error != "" || a.CaptureReadSamples <= 0 || a.CapturedSamples <= 0 || !a.StimulusWindowValid || !a.StimulusAudible || a.EnvelopeCorrelation < acousticMinEnvelopeCorrelation || a.CapturedPeak >= .999 {
		return errors.New("acoustic boundary: unhealthy or unvalidated microphone capture")
	}
	if len(r.Responses) != 1 || len(r.Metrics) != 1 {
		return errors.New("acoustic boundary: missing or duplicate response/terminal metrics")
	}
	response, m := r.Responses[0], r.Metrics[0]
	if response.Interrupted || m.Interrupted || m.Outcome != "completed" || (r.Name != "failure" && strings.TrimSpace(response.Response) == "") {
		return errors.New("acoustic boundary: incomplete response")
	}
	if r.Name == "failure" {
		if !response.Degraded || !m.Degraded || response.Response != "" || len(r.Errors) != 1 || r.Errors[0].Stage != "brain" || !strings.Contains(r.Errors[0].Message, acousticProviderFailure) {
			return errors.New("acoustic boundary: provider failure not publicly reported as one degraded recovery")
		}
	} else if response.Degraded || m.Degraded || len(r.Errors) != 0 {
		return errors.New("acoustic boundary: unexpected degradation/error")
	}
	if r.Name == "muted" || r.Name == "failure" {
		if m.AudibleSink != "none" || r.PlaybackStarts != 0 || r.PlaybackCompletions != 0 || a.NonzeroReferenceSamples != 0 {
			return errors.New("acoustic boundary: silent response rendered or claimed audible delivery")
		}
		return nil
	}
	if r.Name != "failure" && r.Name != "recovered" {
		return errors.New("acoustic boundary: unknown phase")
	}
	if m.AudibleSink != "local_speaker" || r.PlaybackStarts <= 0 || r.PlaybackStarts != r.PlaybackCompletions || a.NonzeroReferenceSamples <= 0 {
		return errors.New("acoustic boundary: missing completed physical response")
	}
	r.AudibleDeliveryCompleted = true
	return nil
}
