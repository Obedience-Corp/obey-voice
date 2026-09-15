package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio/playback"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/stt"
)

type acousticResponseReport struct {
	InitialWAV           string                 `json:"initial_wav"`
	Expected             string                 `json:"expected_initial"`
	InitialTranscript    string                 `json:"initial_transcript"`
	ResponseStarted      time.Time              `json:"response_started"`
	ResponseCompleted    time.Time              `json:"response_completed"`
	ResponseSegments     int                    `json:"response_segments"`
	ResponseGain         float64                `json:"effective_response_gain"`
	NonzeroRenderSamples int                    `json:"nonzero_render_reference_samples"`
	QuietStarted         time.Time              `json:"quiet_started"`
	QuietEnded           time.Time              `json:"quiet_ended"`
	QuietDuration        time.Duration          `json:"requested_quiet_duration_ns"`
	QuietReadSamples     int64                  `json:"quiet_read_samples"`
	QuietMinimumSamples  int64                  `json:"quiet_minimum_samples"`
	NoEchoObserved       bool                   `json:"no_echo_observed"`
	UnexpectedInput      string                 `json:"unexpected_input,omitempty"`
	Error                string                 `json:"error,omitempty"`
	StimulusDevice       string                 `json:"stimulus_device"`
	NegotiatedCapture    audio.NegotiatedDevice `json:"negotiated_capture"`
	NegotiatedResponse   audio.NegotiatedDevice `json:"negotiated_response"`
	NegotiatedStimulus   audio.NegotiatedDevice `json:"negotiated_stimulus"`
}

// runAcousticResponse exercises ordinary response barge-in with no interrupt
// stimulus, then reopens the ordinary next listen. All five capture-related
// pipeline dependencies point to the same real microphone for both turns.
func (r *liveRunner) runAcousticResponse(ctx context.Context, initialWAV, expected, artifactDir string, quiet time.Duration) (runErr error) {
	if ctx == nil || r == nil || r.agent == nil || r.agent.Pipeline == nil || r.cfg == nil || r.player == nil {
		return errors.New("acoustic response: agent and audio configuration are required")
	}
	if initialWAV == "" || normalizeAcousticText(expected) == "" || artifactDir == "" || quiet <= 0 {
		return errors.New("acoustic response: fixture, expected text, artifacts and positive quiet duration are required")
	}
	initial, rate, err := audio.ReadWAVFloat32(initialWAV)
	if err != nil || len(initial) == 0 || rate <= 0 {
		return fmt.Errorf("acoustic response: invalid initial WAV: %w", errors.Join(err, errors.New("need non-empty PCM")))
	}
	if err := os.MkdirAll(artifactDir, 0o700); err != nil {
		return err
	}
	p := r.agent.Pipeline
	if p.Events == nil {
		return errors.New("acoustic response: missing event bus")
	}
	response, ok := r.player.real.(*playback.Player)
	if !ok {
		return errors.New("acoustic response: physical response player required")
	}
	device := stimulusOutputDevice(r.cfg, r.stimulusDevice)
	report := acousticResponseReport{InitialWAV: initialWAV, Expected: expected, QuietDuration: quiet, StimulusDevice: acousticDeviceLabel(device), ResponseGain: response.Volume()}
	capture := audio.NewCaptureWithDevice(r.cfg.InputDevice)
	stimulus := playback.NewPlayerWithDevice(device)
	var raw *rawCaptureFrontend
	var mic []float32
	var micMu sync.Mutex
	defer func() {
		if runErr != nil {
			report.Error = runErr.Error()
		}
		report.NegotiatedCapture, report.NegotiatedResponse, report.NegotiatedStimulus = capture.NegotiatedDevice(), response.NegotiatedDevice(), stimulus.NegotiatedDevice()
		errs := []error{audio.WriteWAVFloat32(filepath.Join(artifactDir, "initial.wav"), rate, initial)}
		micMu.Lock()
		processed := append([]float32(nil), mic...)
		micMu.Unlock()
		if len(processed) > 0 {
			errs = append(errs, audio.WriteWAVFloat32(filepath.Join(artifactDir, "mic-processed.wav"), audio.SampleRate, processed))
		}
		if raw != nil {
			errs = append(errs, raw.WriteTrace(filepath.Join(artifactDir, "frontend.jsonl")))
			if samples := raw.RawSnapshot(); len(samples) > 0 {
				errs = append(errs, audio.WriteWAVFloat32(filepath.Join(artifactDir, "mic-in.wav"), audio.SampleRate, samples))
			}
		}
		data, err := json.MarshalIndent(report, "", "  ")
		if err == nil {
			err = os.WriteFile(filepath.Join(artifactDir, "response.json"), append(data, '\n'), 0o600)
		}
		runErr = errors.Join(runErr, errors.Join(errs...), err)
	}()
	defer stimulus.Close()
	if err := requireEchoResponseVolume(response); err != nil {
		return err
	}
	frontend, err := interruptionFrontend(r.cfg)
	if err != nil {
		return err
	}
	defer frontend.Close()
	raw = newRawCaptureFrontend(frontend)
	raw.EnableTrace(true)
	capture.SetFrontend(raw)
	response.SetFrontend(raw)
	defer response.SetFrontend(nil)
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
	endable := audio.NewFinalizable(frames)
	recognizer, cleanup, err := stt.NewProvider(r.cfg, endable, vad)
	if err != nil {
		return err
	}
	if cleanup != nil {
		defer cleanup()
	}
	id, stream := capture.Subscribe(512)
	collected := make(chan struct{})
	go func() {
		defer close(collected)
		for samples := range stream {
			micMu.Lock()
			mic = append(mic, samples...)
			micMu.Unlock()
		}
	}()
	defer func() { capture.Unsubscribe(id); <-collected }()
	if err := capture.Start(ctx); err != nil {
		return err
	}
	defer capture.Stop()
	oldCapture, oldVAD, oldBarge, oldSTT, oldEnd := p.Capture, p.VAD, p.BargeInVAD, p.STT, p.Utterance
	p.Capture, p.VAD, p.BargeInVAD, p.STT, p.Utterance = capture, vad, barge, recognizer, endable
	defer func() {
		p.Capture, p.VAD, p.BargeInVAD, p.STT, p.Utterance = oldCapture, oldVAD, oldBarge, oldSTT, oldEnd
	}()
	if err := runEchoOnlyResponse(ctx, p.Events, p.RunTurn, func(playCtx context.Context) error {
		return playAcousticStimulus(playCtx, stimulus, initial, rate)
	}, frames.readSamples.Load, &report); err != nil {
		return err
	}
	// The player references the callback's post-volume PCM. Initial fixture
	// playback has no reference, so nonzero reference here must come from the
	// response. Device events alone would also succeed for all-zero playback.
	for _, event := range raw.TraceSnapshot() {
		if event.Op == "reference" {
			for _, sample := range event.Samples {
				if sample != 0 {
					report.NonzeroRenderSamples++
				}
			}
		}
	}
	if report.NonzeroRenderSamples == 0 {
		report.NoEchoObserved = false
		return errors.New("acoustic response: no nonzero rendered response samples")
	}
	return nil
}

func requireEchoResponseVolume(response *playback.Player) error {
	if response == nil || response.Volume() <= 0 {
		return errors.New("acoustic response: effective response playback volume must be positive")
	}
	return nil
}

// Count actual STT reads, retaining the source's gain and metadata. Known ring
// overflow or a reopened device invalidates this control instead of looking
// like silence. Deliberate pipeline reset/preroll retention remains ordinary.
type responseCaptureSource struct {
	audio.FrameSource
	gain        audio.GainReporter
	readSamples atomic.Int64
	generation  uint64 // accessed only by the joined, sequential STT reader
}

func (s *responseCaptureSource) ReadFrame(ctx context.Context) (audio.Frame, error) {
	frame, err := s.FrameSource.ReadFrame(ctx)
	if err != nil {
		return frame, err
	}
	if frame.CaptureGap.OverflowSamples > 0 {
		return frame, fmt.Errorf("acoustic response: capture overflow lost %d samples", frame.CaptureGap.OverflowSamples)
	}
	for _, span := range frame.CaptureSpans {
		if s.generation != 0 && span.DeviceGeneration != s.generation {
			return frame, errors.New("acoustic response: capture device changed generation")
		}
		s.generation = span.DeviceGeneration
	}
	s.readSamples.Add(int64(len(frame.Samples)))
	return frame, nil
}

func (s *responseCaptureSource) InputGain() float64 { return s.gain.InputGain() }

// runEchoOnlyResponse keeps the evidence/cleanup contract testable without
// models or devices. Only the known initial stimulus is played.
func runEchoOnlyResponse(ctx context.Context, bus *events.Bus, run func(context.Context) (string, error), play func(context.Context) error, readSamples func() int64, report *acousticResponseReport) error {
	if ctx == nil || bus == nil || run == nil || play == nil || readSamples == nil || report == nil || report.QuietDuration <= 0 {
		return errors.New("acoustic response: incomplete control dependencies")
	}
	initial := &responseObservation{report: report}
	text, err := runAcousticTurn(ctx, bus, func(turnCtx context.Context) (string, error) {
		return initial.run(turnCtx, bus, run)
	}, play)
	report.InitialTranscript = text
	if err != nil {
		return err
	}
	if normalizeAcousticText(text) != normalizeAcousticText(report.Expected) || initial.inputs != 1 {
		return fmt.Errorf("acoustic response: initial transcript %q does not match %q (input events=%d)", text, report.Expected, initial.inputs)
	}
	if initial.started == 0 || initial.completed != initial.started || initial.metrics == 0 || initial.ready == 0 {
		return fmt.Errorf("acoustic response: incomplete physical response evidence: starts=%d completions=%d metrics=%d ready=%d", initial.started, initial.completed, initial.metrics, initial.ready)
	}
	return observeQuietNextTurn(ctx, bus, run, readSamples, report)
}

// Observation callbacks run before fanout, cancel promptly on failure, and do
// not wait on the turn. Their run wrapper returns only after RunTurn joins.
type responseObservation struct {
	mu                                         sync.Mutex
	report                                     *acousticResponseReport
	quiet                                      bool
	listening                                  chan struct{}
	listenOnce                                 sync.Once
	inputs, started, completed, metrics, ready int
	err                                        error
}

func (o *responseObservation) run(ctx context.Context, bus *events.Bus, run func(context.Context) (string, error)) (string, error) {
	child, cancel := context.WithCancel(ctx)
	defer cancel()
	remove := bus.ObserveBeforeFanout(func(event events.Event) {
		o.mu.Lock()
		defer o.mu.Unlock()
		fail := func(err error) {
			if o.err == nil {
				o.err = err
			}
			cancel()
		}
		switch e := event.(type) {
		case events.STTPhase:
			if o.quiet && e.Phase == "listening" {
				o.listenOnce.Do(func() { o.report.QuietStarted = time.Now(); close(o.listening) })
			} else if o.quiet && (e.Phase == "hearing" || e.Phase == "transcribing") {
				fail(fmt.Errorf("acoustic response: unexpected %s during echo-only next listen", e.Phase))
			}
		case events.UserInput:
			o.inputs++
			if o.quiet || o.inputs != 1 || normalizeAcousticText(e.Text) != normalizeAcousticText(o.report.Expected) {
				o.report.UnexpectedInput = e.Text
				fail(fmt.Errorf("acoustic response: unexpected user input %q", e.Text))
			}
		case events.SpeakingInterrupted:
			fail(fmt.Errorf("acoustic response: self-interruption: %s", e.Reason))
		case events.SpeakingStarted:
			o.started++
			if o.report.ResponseStarted.IsZero() {
				o.report.ResponseStarted = time.Now()
			}
			if o.quiet || e.AudibleSink != "local_speaker" {
				fail(errors.New("acoustic response: unexpected/nonphysical playback"))
			}
		case events.SpeakingComplete:
			o.completed++
			o.report.ResponseSegments = o.completed
			o.report.ResponseCompleted = time.Now()
			if o.quiet || e.Interrupted || e.AudibleSink != "local_speaker" || e.Elapsed <= 0 {
				fail(errors.New("acoustic response: playback did not complete successfully"))
			}
		case events.ResponseReady:
			o.ready++
			if o.quiet || e.Interrupted || e.Degraded {
				fail(errors.New("acoustic response: interrupted/degraded/unexpected response"))
			}
		case events.TurnMetrics:
			o.metrics++
			if !o.quiet && (e.Outcome != "completed" || e.Interrupted || e.Degraded || e.AudibleSink != "local_speaker") {
				fail(fmt.Errorf("acoustic response: unsuccessful response outcome %q", e.Outcome))
			}
			if o.quiet && (e.Outcome != "interrupted" || e.Degraded) {
				fail(fmt.Errorf("acoustic response: unexpected next-listen outcome %q", e.Outcome))
			}
		case events.Error:
			fail(fmt.Errorf("acoustic response: %s: %s", e.Stage, e.Message))
		}
	})
	defer remove()
	text, err := run(child)
	o.mu.Lock()
	defer o.mu.Unlock()
	return text, errors.Join(o.err, err)
}

func observeQuietNextTurn(ctx context.Context, bus *events.Bus, run func(context.Context) (string, error), readSamples func() int64, report *acousticResponseReport) error {
	child, cancel := context.WithCancel(ctx)
	defer cancel()
	o := &responseObservation{report: report, quiet: true, listening: make(chan struct{})}
	type outcome struct {
		text string
		err  error
	}
	done := make(chan outcome, 1)
	go func() { text, err := o.run(child, bus, run); done <- outcome{text, err} }()
	joined := false
	defer func() {
		cancel()
		if !joined {
			<-done
		}
	}()
	early := func(result outcome) error {
		joined = true
		return fmt.Errorf("acoustic response: next listen ended before observation completed: input=%q: %w", result.text, errors.Join(result.err, errors.New("early turn termination is not silence evidence")))
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case result := <-done:
		return early(result)
	case <-o.listening:
	}
	before := readSamples()
	timer := time.NewTimer(report.QuietDuration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case result := <-done:
		return early(result)
	case <-timer.C:
	}
	// Do not turn a concurrent timeout/provider return into successful silence.
	select {
	case result := <-done:
		return early(result)
	default:
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	report.QuietEnded = time.Now()
	report.QuietReadSamples = readSamples() - before
	// ReadFrame releases full ChunkSize frames; allow its single buffered
	// trailing frame, rather than accepting an empty or stalled capture.
	report.QuietMinimumSamples = max(1, int64(report.QuietDuration.Seconds()*audio.SampleRate)-audio.ChunkSize)
	cancel()
	result := <-done
	joined = true
	if result.text != "" || !responseCancellationOnly(result.err) {
		return fmt.Errorf("acoustic response: next listen did not join expected harness cancellation: input=%q err=%v", result.text, result.err)
	}
	o.mu.Lock()
	observationErr := o.err
	o.mu.Unlock()
	if observationErr != nil {
		return observationErr
	}
	if report.QuietReadSamples < report.QuietMinimumSamples {
		return fmt.Errorf("acoustic response: insufficient capture during quiet window: read=%d minimum=%d", report.QuietReadSamples, report.QuietMinimumSamples)
	}
	report.NoEchoObserved = true
	return nil
}

// errors.Is alone would accept cancellation joined with a hardware/provider
// failure. Only cancellation wrappers from our completed window are expected.
func responseCancellationOnly(err error) bool {
	if err == context.Canceled {
		return true
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		children := joined.Unwrap()
		if len(children) == 0 {
			return false
		}
		for _, child := range children {
			if !responseCancellationOnly(child) {
				return false
			}
		}
		return true
	}
	if wrapped, ok := err.(interface{ Unwrap() error }); ok {
		return responseCancellationOnly(wrapped.Unwrap())
	}
	return false
}
