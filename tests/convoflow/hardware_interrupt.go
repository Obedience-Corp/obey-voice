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
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio/apmfrontend"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/stt"
)

type interruptionReport struct {
	InitialWAV            string                 `json:"initial_wav"`
	InterruptWAV          string                 `json:"interrupt_wav"`
	Expected              string                 `json:"expected_interrupt"`
	ResponseStarted       time.Time              `json:"response_started,omitempty"`
	StimulusStarted       time.Time              `json:"stimulus_started,omitempty"`
	InterruptedAfter      time.Duration          `json:"stimulus_to_interrupt_ns"`
	PlaybackStoppedAfter  time.Duration          `json:"stimulus_to_playback_stopped_ns"`
	DetectionToStop       time.Duration          `json:"detection_to_playback_stopped_ns"`
	ProductionInterrupted bool                   `json:"production_interrupted"`
	InitialTranscript     string                 `json:"initial_transcript"`
	Transcript            string                 `json:"transcript,omitempty"`
	Error                 string                 `json:"error,omitempty"`
	NegotiatedCapture     audio.NegotiatedDevice `json:"negotiated_capture"`
	NegotiatedResponse    audio.NegotiatedDevice `json:"negotiated_response"`
	NegotiatedStimulus    audio.NegotiatedDevice `json:"negotiated_stimulus"`
}

// runAcousticInterruption exercises the production capture/VAD/barge-in path.
// Initial speech is a real WAV played through the speaker; the interrupt WAV
// is played by a separate player with no AEC reference. Production RunTurn's
// watcher owns interruption and keepCapture; this helper never calls
// StopPlayback or cancels the turn on detected speech.
func (r *liveRunner) runAcousticInterruption(ctx context.Context, initialWAV, interruptWAV, expected, artifactDir string) (runErr error) {
	if ctx == nil {
		return errors.New("acoustic interruption: nil context")
	}
	if r == nil || r.agent == nil || r.agent.Pipeline == nil {
		return errors.New("acoustic interruption: agent is not configured")
	}
	if r.cfg == nil || r.player == nil {
		return errors.New("acoustic interruption: audio configuration is not ready")
	}
	if initialWAV == "" || interruptWAV == "" || expected == "" || artifactDir == "" {
		return errors.New("acoustic interruption: initial WAV, interrupt WAV, expected text and artifacts are required")
	}
	initial, initialRate, err := audio.ReadWAVFloat32(initialWAV)
	if err != nil {
		return fmt.Errorf("initial WAV: %w", err)
	}
	interrupt, interruptRate, err := audio.ReadWAVFloat32(interruptWAV)
	if err != nil {
		return fmt.Errorf("interrupt WAV: %w", err)
	}
	if len(initial) == 0 || len(interrupt) == 0 || initialRate <= 0 || interruptRate <= 0 {
		return errors.New("acoustic interruption: empty WAV")
	}
	if err := os.MkdirAll(artifactDir, 0o700); err != nil {
		return err
	}
	report := interruptionReport{InitialWAV: initialWAV, InterruptWAV: interruptWAV, Expected: expected}
	var rawFrontend *rawCaptureFrontend
	var mic []float32
	var capture *audio.Capture
	var stimulus *audio.Player
	var response *audio.Player
	var micMu sync.Mutex
	defer func() {
		if runErr != nil {
			report.Error = runErr.Error()
		}
		micMu.Lock()
		captured := append([]float32(nil), mic...)
		micMu.Unlock()
		var errs []error
		errs = append(errs, audio.WriteWAVFloat32(filepath.Join(artifactDir, "initial.wav"), initialRate, initial))
		errs = append(errs, audio.WriteWAVFloat32(filepath.Join(artifactDir, "interrupt.wav"), interruptRate, interrupt))
		if len(captured) > 0 {
			errs = append(errs, audio.WriteWAVFloat32(filepath.Join(artifactDir, "mic-processed.wav"), audio.SampleRate, captured))
		}
		if rawFrontend != nil {
			errs = append(errs, rawFrontend.WriteTrace(filepath.Join(artifactDir, "frontend.jsonl")))
			if raw := rawFrontend.RawSnapshot(); len(raw) > 0 {
				errs = append(errs, audio.WriteWAVFloat32(filepath.Join(artifactDir, "mic-in.wav"), audio.SampleRate, raw))
			}
		}
		if capture != nil {
			report.NegotiatedCapture = capture.NegotiatedDevice()
		}
		if response != nil {
			report.NegotiatedResponse = response.NegotiatedDevice()
		}
		if stimulus != nil {
			report.NegotiatedStimulus = stimulus.NegotiatedDevice()
		}
		b, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			errs = append(errs, err)
		} else {
			errs = append(errs, os.WriteFile(filepath.Join(artifactDir, "interruption.json"), append(b, '\n'), 0600))
		}
		runErr = errors.Join(runErr, errors.Join(errs...))
	}()
	p := r.agent.Pipeline
	if p.Events == nil {
		return errors.New("acoustic interruption: event bus unavailable")
	}
	real, ok := r.player.real.(*audio.Player)
	if !ok {
		return errors.New("acoustic interruption: physical response player required")
	}
	response = real
	capture = audio.NewCaptureWithDevice(r.cfg.InputDevice)
	frontend, err := interruptionFrontend(r.cfg)
	if err != nil {
		return err
	}
	defer frontend.Close()
	rawFrontend = newRawCaptureFrontend(frontend)
	rawFrontend.EnableTrace(true)
	capture.SetFrontend(rawFrontend)
	real.SetFrontend(rawFrontend)
	defer real.SetFrontend(nil)
	vad, err := audio.NewVAD(r.cfg)
	if err != nil {
		return err
	}
	defer vad.Delete()
	barge, err := audio.NewBargeInVAD(r.cfg)
	if err != nil {
		return err
	}
	defer barge.Delete()
	endable := audio.NewFinalizable(capture)
	recognizer, cleanup, err := stt.NewProvider(r.cfg, endable, vad)
	if err != nil {
		return err
	}
	if cleanup != nil {
		defer cleanup()
	}
	streamID, micStream := capture.Subscribe(512)
	micDone := make(chan struct{})
	go func() {
		defer close(micDone)
		for chunk := range micStream {
			micMu.Lock()
			mic = append(mic, chunk...)
			micMu.Unlock()
		}
	}()
	defer func() { capture.Unsubscribe(streamID); <-micDone }()
	if err := capture.Start(ctx); err != nil {
		return err
	}
	defer capture.Stop()
	oldCapture, oldVAD, oldBarge, oldSTT, oldEnd := p.Capture, p.VAD, p.BargeInVAD, p.STT, p.Utterance
	p.Capture, p.VAD, p.BargeInVAD, p.STT, p.Utterance = capture, vad, barge, recognizer, endable
	defer func() {
		p.Capture, p.VAD, p.BargeInVAD, p.STT, p.Utterance = oldCapture, oldVAD, oldBarge, oldSTT, oldEnd
	}()
	type observed struct {
		at    time.Time
		event events.Event
	}
	observedCh := make(chan observed, 64)
	remove := p.Events.SubscribeAll(func(e events.Event) {
		switch e.(type) {
		case events.SpeakingStarted, events.SpeakingInterrupted, events.TurnMetrics:
			select {
			case observedCh <- observed{time.Now(), e}:
			default:
			}
		}
	})
	defer remove()
	runCtx, cancel := context.WithCancel(ctx)
	type outcome struct {
		text string
		err  error
	}
	done := make(chan outcome, 1)
	running := true
	defer func() {
		cancel()
		if running {
			<-done
		}
	}()
	readyInput, err := newHardwareReadiness(p.Events)
	if err != nil {
		return err
	}
	defer readyInput.close()
	go func() { text, err := p.RunTurn(runCtx); done <- outcome{text, err} }()
	if err := readyInput.wait(runCtx); err != nil {
		return err
	}
	stimulus = audio.NewPlayerWithDevice(r.cfg.OutputDevice)
	defer stimulus.Close()
	if err := playAcousticStimulus(runCtx, stimulus, initial, initialRate); err != nil {
		return err
	}
	// SpeakingStarted is emitted from the production player's actual start.
	waiting := true
	for waiting {
		select {
		case <-runCtx.Done():
			return runCtx.Err()
		case result := <-done:
			running = false
			return fmt.Errorf("initial voice turn ended before playback: transcript=%q err=%v", result.text, result.err)
		case observation := <-observedCh:
			switch event := observation.event.(type) {
			case events.SpeakingStarted:
				if event.AudibleSink != "local_speaker" {
					return errors.New("response did not use physical speaker")
				}
				report.ResponseStarted = observation.at
				waiting = false
			case events.SpeakingInterrupted:
				return errors.New("response interrupted before test stimulus")
			}
		}
	}
	// Wait beyond the production 600ms arm delay to reject self-interruption
	// before external speech can be mistaken for the cause.
	quiet := time.NewTimer(900 * time.Millisecond)
	defer quiet.Stop()
	select {
	case <-runCtx.Done():
		return runCtx.Err()
	case result := <-done:
		running = false
		return fmt.Errorf("response ended before interrupt stimulus: %v", result.err)
	case <-quiet.C:
	}
	for {
		select {
		case observation := <-observedCh:
			if _, ok := observation.event.(events.SpeakingInterrupted); ok {
				return errors.New("response self-interrupted before stimulus")
			}
		default:
			goto ready
		}
	}
ready:
	stimulusDone := make(chan error, 1)
	stimulusStarted := make(chan time.Time, 1)
	go func() {
		stimulusDone <- playAcousticStimulusObserved(runCtx, stimulus, interrupt, interruptRate, func(at time.Time) { stimulusStarted <- at })
	}()
	stimulusRunning := true
	defer func() {
		if stimulusRunning {
			cancel()
			<-stimulusDone
		}
	}()
	select {
	case report.StimulusStarted = <-stimulusStarted:
	case err := <-stimulusDone:
		stimulusRunning = false
		return fmt.Errorf("interrupt stimulus did not start: %v", err)
	case <-runCtx.Done():
		return runCtx.Err()
	}
	var first outcome
	gotFirst := false
	for !gotFirst {
		select {
		case <-runCtx.Done():
			return runCtx.Err()
		case observation := <-observedCh:
			if ev, ok := observation.event.(events.SpeakingInterrupted); ok {
				if !strings.Contains(strings.ToLower(ev.Reason), "barge") {
					return fmt.Errorf("unexpected interruption reason %q", ev.Reason)
				}
				report.InterruptedAfter = observation.at.Sub(report.StimulusStarted)
				report.PlaybackStoppedAfter = report.InterruptedAfter
				report.DetectionToStop = ev.DetectionToPlaybackStop
			}
			if ev, ok := observation.event.(events.TurnMetrics); ok {
				report.ProductionInterrupted = ev.Interrupted
			}
		case first = <-done:
			running = false
			gotFirst = true
		}
	}
	// The event bus emits synchronously before RunTurn returns; drain final metrics.
	for {
		select {
		case observation := <-observedCh:
			switch ev := observation.event.(type) {
			case events.TurnMetrics:
				report.ProductionInterrupted = ev.Interrupted
			case events.SpeakingInterrupted:
				report.InterruptedAfter = observation.at.Sub(report.StimulusStarted)
				report.PlaybackStoppedAfter = report.InterruptedAfter
				report.DetectionToStop = ev.DetectionToPlaybackStop
			}
		default:
			goto drained
		}
	}
drained:
	report.InitialTranscript = first.text
	// SpeakingInterrupted is emitted synchronously after Player.Stop returns.
	// The stored delay is its observation boundary, not turn cleanup time.
	if err := <-stimulusDone; err != nil {
		stimulusRunning = false
		return err
	}
	stimulusRunning = false
	if real.IsPlaying() || !report.ProductionInterrupted || report.InterruptedAfter <= 0 || report.DetectionToStop <= 0 {
		return fmt.Errorf("production interruption not proven: interrupted=%t detector=%s playing=%t turn_error=%v", report.ProductionInterrupted, report.InterruptedAfter, real.IsPlaying(), first.err)
	}
	// No capture reset or synthesized transcript: RunTurn consumes keepCapture.
	text, err := p.RunTurn(runCtx)
	report.Transcript = text
	if err != nil {
		return fmt.Errorf("retained turn: %w", err)
	}
	if normalizeAcousticText(text) != normalizeAcousticText(expected) {
		return fmt.Errorf("retained transcript %q does not match %q", text, expected)
	}
	return nil
}

func interruptionFrontend(cfg *config.Config) (audio.Frontend, error) {
	switch config.VoiceFrontendOf(cfg) {
	case config.VoiceFrontendOff:
		return audio.NewPassthroughFrontend(), nil
	case config.VoiceFrontendNLMS:
		return audio.NewVoiceFrontend(), nil
	case config.VoiceFrontendWebRTC:
		if err := config.ValidateVoiceFrontend(cfg); err != nil {
			return nil, err
		}
		return apmfrontend.New(config.VoiceFrontendNSLevelOf(cfg))
	default:
		return nil, fmt.Errorf("unsupported voice front-end %q", config.VoiceFrontendOf(cfg))
	}
}
