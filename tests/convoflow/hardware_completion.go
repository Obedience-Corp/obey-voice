package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio/playback"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/pipeline"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/stt"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/work"
)

type completionReport struct {
	Mode               string                 `json:"mode,omitempty"`
	Fixture            string                 `json:"fixture"`
	Expected           string                 `json:"expected"`
	StimulusDevice     string                 `json:"stimulus_device"`
	WorkerReadinessMS  float64                `json:"worker_readiness_ms"`
	PauseMS            float64                `json:"pause_ms"`
	CompletionMS       float64                `json:"completion_ms"`
	Acknowledged       bool                   `json:"acknowledged"`
	Yielded            bool                   `json:"yielded"`
	Communicated       bool                   `json:"communicated"`
	InitialTranscript  string                 `json:"initial_transcript"`
	Transcript         string                 `json:"transcript,omitempty"`
	Error              string                 `json:"error,omitempty"`
	NegotiatedCapture  audio.NegotiatedDevice `json:"negotiated_capture"`
	NegotiatedResponse audio.NegotiatedDevice `json:"negotiated_response"`
	NegotiatedStimulus audio.NegotiatedDevice `json:"negotiated_stimulus"`
}

// runAcousticCompletion proves the passive completion handoff over the real
// capture path. The worker is admitted through the owning Agency; pause,
// yield, completion, receipt and resumed listening all remain production API
// operations. No synthetic user event or manual cancellation is used.
func (r *liveRunner) runAcousticCompletion(ctx context.Context, fixtureWAV, expected, artifactDir string) error {
	return r.runAcousticCompletionMode(ctx, fixtureWAV, expected, artifactDir, "")
}

func (r *liveRunner) runAcousticCompletionBoundary(ctx context.Context, mode, fixtureWAV, expected, artifactDir string) error {
	if mode != "acoustic_completion_mute" && mode != "acoustic_completion_speech" {
		return fmt.Errorf("unknown completion boundary %q", mode)
	}
	return r.runAcousticCompletionMode(ctx, fixtureWAV, expected, artifactDir, mode)
}

func (r *liveRunner) runAcousticCompletionMode(ctx context.Context, fixtureWAV, expected, artifactDir, mode string) (runErr error) {
	if ctx == nil {
		return errors.New("acoustic completion: nil context")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if r == nil || r.agent == nil || r.agent.Pipeline == nil {
		return errors.New("acoustic completion: agent is not configured")
	}
	if r.cfg == nil || r.player == nil {
		return errors.New("acoustic completion: audio configuration is not ready")
	}
	if fixtureWAV == "" || expected == "" || artifactDir == "" {
		return errors.New("acoustic completion: fixture, expected text and artifacts are required")
	}
	samples, rate, err := audio.ReadWAVFloat32(fixtureWAV)
	if err != nil {
		return fmt.Errorf("fixture: %w", err)
	}
	if len(samples) == 0 || rate <= 0 {
		return errors.New("acoustic completion: fixture is empty")
	}
	if mode != "" {
		if err := os.MkdirAll(filepath.Dir(artifactDir), 0o700); err != nil {
			return err
		}
		if err := os.Mkdir(artifactDir, 0o700); err != nil {
			return err
		}
	} else if err := os.MkdirAll(artifactDir, 0o700); err != nil {
		return err
	}
	stimulusDevice := stimulusOutputDevice(r.cfg, r.stimulusDevice)
	report := completionReport{Mode: mode, Fixture: fixtureWAV, Expected: expected, StimulusDevice: acousticDeviceLabel(stimulusDevice)}
	var rawFrontend *rawCaptureFrontend
	var mic []float32
	var micMu sync.Mutex
	var capture *audio.Capture
	var stimulus *playback.Player
	var response *playback.Player
	defer func() {
		if runErr != nil {
			report.Error = runErr.Error()
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
		micMu.Lock()
		captured := append([]float32(nil), mic...)
		micMu.Unlock()
		var errs []error
		errs = append(errs, audio.WriteWAVFloat32(filepath.Join(artifactDir, "fixture.wav"), rate, samples))
		if len(captured) > 0 {
			errs = append(errs, audio.WriteWAVFloat32(filepath.Join(artifactDir, "mic-processed.wav"), audio.SampleRate, captured))
		}
		if rawFrontend != nil {
			errs = append(errs, rawFrontend.WriteTrace(filepath.Join(artifactDir, "frontend.jsonl")))
			if raw := rawFrontend.RawSnapshot(); len(raw) > 0 {
				errs = append(errs, audio.WriteWAVFloat32(filepath.Join(artifactDir, "mic-in.wav"), audio.SampleRate, raw))
			}
		}
		b, e := json.MarshalIndent(report, "", "  ")
		if e != nil {
			errs = append(errs, e)
		} else {
			errs = append(errs, os.WriteFile(filepath.Join(artifactDir, "completion.json"), append(b, '\n'), 0600))
		}
		runErr = errors.Join(runErr, errors.Join(errs...))
	}()

	p := r.agent.Pipeline
	if p.Agency == nil {
		return errors.New("acoustic completion: Agency is not configured")
	}
	if r.worker != nil {
		if _, sustained := r.worker.inner.(*sustainedLocalWorker); sustained {
			return errors.New("acoustic completion: sustained worker is not valid for completion acceptance")
		}
	}
	capture = audio.NewCaptureWithDevice(r.cfg.InputDevice)
	frontend, err := interruptionFrontend(r.cfg)
	if err != nil {
		return err
	}
	defer frontend.Close()
	rawFrontend = newRawCaptureFrontend(frontend)
	rawFrontend.EnableTrace(true)
	capture.SetFrontend(rawFrontend)
	real, ok := r.player.real.(*playback.Player)
	if !ok {
		return errors.New("acoustic completion: physical response player required")
	}
	response = real
	if mode != "" {
		if err := requireEchoResponseVolume(real); err != nil {
			return err
		}
	}
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
	frames := &responseCaptureSource{FrameSource: capture, gain: capture}
	var source audio.FrameSource = capture
	if mode != "" {
		source = frames
	}
	endable := audio.NewFinalizable(source)
	recognizer, cleanup, err := stt.NewProvider(r.cfg, endable, vad)
	if err != nil {
		return err
	}
	if cleanup != nil {
		defer cleanup()
	}
	streamID, stream := capture.Subscribe(512)
	doneCapture := make(chan struct{})
	go func() {
		defer close(doneCapture)
		for chunk := range stream {
			micMu.Lock()
			mic = append(mic, chunk...)
			micMu.Unlock()
		}
	}()
	defer func() { capture.Unsubscribe(streamID); <-doneCapture }()
	if err := capture.Start(ctx); err != nil {
		return err
	}
	defer capture.Stop()
	oldCapture, oldVAD, oldBarge, oldSTT, oldEnd := p.Capture, p.VAD, p.BargeInVAD, p.STT, p.Utterance
	p.Capture, p.VAD, p.BargeInVAD, p.STT, p.Utterance = capture, vad, barge, recognizer, endable
	defer func() {
		p.Capture, p.VAD, p.BargeInVAD, p.STT, p.Utterance = oldCapture, oldVAD, oldBarge, oldSTT, oldEnd
	}()
	stimulus = playback.NewPlayerWithDevice(stimulusDevice)
	defer stimulus.Close()
	if mode != "" {
		runErr = runCompletionBoundary(ctx, p, mode, expected, artifactDir, frames.readSamples.Load,
			func() int { return nonzeroResponseReferenceSamples(rawFrontend) },
			func(playCtx context.Context) (acousticReport, error) {
				start := len(rawFrontend.RawSnapshot())
				reads := frames.readSamples.Load()
				var started time.Time
				err := playAcousticStimulusObserved(playCtx, stimulus, samples, rate, func(at time.Time) { started = at })
				completed := time.Now()
				if err != nil {
					return acousticReport{}, err
				}
				// Include the acoustic tail after the source player finishes; the original
				// capture remains live and its callback/read health is checked separately.
				timer := time.NewTimer(300 * time.Millisecond)
				defer timer.Stop()
				select {
				case <-playCtx.Done():
					return acousticReport{}, playCtx.Err()
				case <-timer.C:
				}
				raw := rawFrontend.RawSnapshot()
				if start > len(raw) {
					return acousticReport{}, errors.New("capture trace reset during stimulus")
				}
				correlation, e := audio.NewFixtureSourceFromWAV(fixtureWAV, audio.ChunkSize, false)
				if e != nil {
					return acousticReport{}, e
				}
				var reference []float32
				for chunk := correlation.Read(); len(chunk) > 0; chunk = correlation.Read() {
					reference = append(reference, chunk...)
				}
				if e := correlation.Close(); e != nil {
					return acousticReport{}, e
				}
				scored := acousticReport{Stimulus: fixtureWAV, ExpectedTranscript: expected, CaptureReadSamples: frames.readSamples.Load() - reads, CaptureExcerptStart: start, StimulusStarted: started, StimulusCompleted: completed}
				err = scoreAcousticCapture(&scored, reference, raw[start:])
				return scored, err
			})
		report.Communicated = runErr == nil
		return runErr
	}
	before := r.workSnapshot()
	known := map[string]struct{}{}
	for _, job := range before.Jobs {
		known[job.Key.JobID] = struct{}{}
	}
	p.Agency.HandleRoute(ctx, nil, nil, pipeline.RouteDecision{Kind: pipeline.RouteSpawn, Transcript: "Check the current local worker completion path and report one short sentence.", Source: "convoflow-completion"})
	jobID := ""
	for until := time.Now().Add(5 * time.Second); jobID == "" && time.Now().Before(until); {
		for _, job := range r.workSnapshot().Jobs {
			if _, ok := known[job.Key.JobID]; !ok && job.Key.JobID != "" {
				jobID = job.Key.JobID
				break
			}
		}
		if jobID == "" {
			time.Sleep(5 * time.Millisecond)
		}
	}
	if jobID == "" {
		return errors.New("completion worker was not admitted")
	}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = r.stopLatencyWorker(cleanupCtx, jobID)
	}()
	type turnResult struct {
		text string
		err  error
	}
	turnDone := make(chan turnResult, 1)
	var turnCtx context.Context
	cancelTurn := func() {}
	var tasks sync.WaitGroup
	// Join all capture users before restoring or closing their providers.
	defer func() {
		cancelTurn()
		tasks.Wait()
		if runErr != nil {
			// A failed scenario aborts this isolated runner. A yielded STT
			// session outlives RunTurn; close it before freeing the recognizer.
			runErr = errors.Join(runErr, p.CloseWork())
		}
	}()
	readiness, err := beginCompletionListenAfterWorker(ctx, p.BackgroundPending, func() {
		turnCtx, cancelTurn = context.WithTimeout(ctx, 45*time.Second)
		tasks.Add(1)
		go func() { defer tasks.Done(); text, e := p.RunTurn(turnCtx); turnDone <- turnResult{text, e} }()
	})
	report.WorkerReadinessMS = float64(readiness.Microseconds()) / 1000
	if err != nil {
		return err
	}
	startPause := time.Now()
	pauseCtx, cancelPause := context.WithTimeout(ctx, 300*time.Millisecond)
	defer cancelPause()
	acknowledged, err := p.PauseForCompletion(pauseCtx)
	for err == nil && !acknowledged && pauseCtx.Err() == nil {
		select {
		case <-pauseCtx.Done():
		case <-time.After(time.Millisecond):
		}
		acknowledged, err = p.PauseForCompletion(pauseCtx)
	}
	report.PauseMS = float64(time.Since(startPause).Microseconds()) / 1000
	report.Acknowledged = acknowledged
	if err != nil {
		return fmt.Errorf("pause for completion: %w", err)
	}
	if !acknowledged || report.PauseMS > 300 {
		return fmt.Errorf("completion pause not acknowledged within 300ms: acknowledged=%t elapsed=%.1fms", acknowledged, report.PauseMS)
	}
	if !p.BackgroundPending() {
		return errors.New("background result disappeared before completion")
	}
	select {
	case result := <-turnDone:
		report.InitialTranscript = strings.TrimSpace(result.text)
		report.Yielded = result.err == nil && report.InitialTranscript == ""
		if result.err != nil {
			return fmt.Errorf("yielded turn: %w", result.err)
		}
		if !report.Yielded {
			return fmt.Errorf("listening turn yielded non-empty transcript %q", result.text)
		}
	case <-turnCtx.Done():
		return errors.New("listening turn did not yield before timeout")
	}
	communicatedBefore := make(map[string]struct{}, len(before.Communicated))
	for _, result := range before.Communicated {
		communicatedBefore[result.Outcome.ResultID] = struct{}{}
	}
	startResume := time.Now()
	if !p.BackgroundPending() {
		return errors.New("background result disappeared before completion")
	}
	if err := p.RunBackgroundTurn(ctx); err != nil {
		return fmt.Errorf("background completion: %w", err)
	}
	report.CompletionMS = float64(time.Since(startResume).Microseconds()) / 1000
	after := r.workSnapshot()
	newCommunicated := 0
	for _, result := range after.Communicated {
		if result.Outcome.JobID == jobID {
			if _, existed := communicatedBefore[result.Outcome.ResultID]; !existed {
				newCommunicated++
			}
		}
	}
	if newCommunicated != 1 {
		return fmt.Errorf("expected exactly one fresh communicated receipt for %s, got %d", jobID, newCommunicated)
	}
	for _, result := range after.Communicated {
		if result.Outcome.JobID == jobID && result.AnnouncementState == work.AnnouncementCommunicated {
			receipt := result.LastAttempt
			if receipt == nil || receipt.Receipt.Mode != "voice" || !receipt.Receipt.Audible || !receipt.Receipt.PlaybackComplete || receipt.Receipt.Segments <= 0 || receipt.Receipt.Interrupted || !receipt.Receipt.ResponseCommitted {
				return fmt.Errorf("worker %s receipt lacks completed audible voice playback", jobID)
			}
		}
	}
	report.Communicated = true
	stimulusDone := make(chan error, 1)
	resumedDone := make(chan struct {
		text string
		err  error
	}, 1)
	readyInput, err := newHardwareReadiness(p.Events)
	if err != nil {
		return err
	}
	defer readyInput.close()
	tasks.Add(1)
	go func() {
		defer tasks.Done()
		text, e := p.RunTurn(turnCtx)
		resumedDone <- struct {
			text string
			err  error
		}{text, e}
	}()
	select {
	case <-readyInput.ready:
	case early := <-resumedDone:
		report.Transcript = strings.TrimSpace(early.text)
		return fmt.Errorf("resumed capture resolved before the test stimulus: text=%q err=%v", early.text, early.err)
	case <-turnCtx.Done():
		return turnCtx.Err()
	}
	tasks.Add(1)
	go func() { defer tasks.Done(); stimulusDone <- playAcousticStimulus(turnCtx, stimulus, samples, rate) }()
	var resumed struct {
		text string
		err  error
	}
	select {
	case resumed = <-resumedDone:
	case <-turnCtx.Done():
		return turnCtx.Err()
	}
	text, err := resumed.text, resumed.err
	report.Transcript = strings.TrimSpace(text)
	if err != nil {
		return fmt.Errorf("resumed turn: %w", err)
	}
	if normalizeAcousticText(text) != normalizeAcousticText(expected) {
		return fmt.Errorf("resumed transcript %q does not match %q", text, expected)
	}
	if err := <-stimulusDone; err != nil {
		return err
	}
	if len(r.workSnapshot().Communicated) != len(after.Communicated) {
		return errors.New("unrelated speech changed communicated result count")
	}
	return nil
}

// beginCompletionListenAfterWorker keeps cold worker readiness outside the
// isolated listen lifetime and the subsequent 300ms pause measurement. This
// control measures completion playback/next-input retention; the separate
// completion-speech control exercises speech during completion generation.
// start must only launch the listening turn, without waiting for it to finish.
func beginCompletionListenAfterWorker(ctx context.Context, pending func() bool, start func()) (time.Duration, error) {
	began := time.Now()
	deadline := time.NewTimer(30 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return time.Since(began), err
		}
		if pending() {
			waited := time.Since(began)
			start()
			return waited, nil
		}
		select {
		case <-ctx.Done():
			return time.Since(began), ctx.Err()
		case <-deadline.C:
			return time.Since(began), errors.New("background result did not become pending")
		case <-ticker.C:
		}
	}
}
