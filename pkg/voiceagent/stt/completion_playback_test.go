package stt

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/endpoint"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/work"
)

type playbackTestFrames struct {
	mu           sync.Mutex
	queue        []audio.Frame
	reads        chan audio.Frame
	retentions   int
	retainResult audio.Frame
	retainErr    error
}

func (f *playbackTestFrames) push(frames ...audio.Frame) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.queue = append(f.queue, frames...)
}
func (f *playbackTestFrames) ReadFrame(ctx context.Context) (audio.Frame, error) {
	if err := ctx.Err(); err != nil {
		return audio.Frame{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.queue) == 0 {
		return audio.Frame{}, audio.ErrNoFrameReady
	}
	frame := f.queue[0]
	f.queue = f.queue[1:]
	f.reads <- frame
	return frame, nil
}
func (*playbackTestFrames) Close() error { return nil }
func (f *playbackTestFrames) RetainForNextListen(audio.Frame, time.Duration) (audio.Frame, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.retentions++
	if f.retainErr != nil {
		return audio.Frame{}, f.retainErr
	}
	f.queue = nil
	return f.retainResult, nil
}

type playbackTestSegmenter struct {
	pcm             []float32
	speech, flushed bool
}

func (s *playbackTestSegmenter) AcceptWaveform(p []float32) {
	s.pcm = append(s.pcm, p...)
	s.speech = frameRMS(p) > .01
}
func (s *playbackTestSegmenter) IsSpeech() bool    { return s.speech }
func (s *playbackTestSegmenter) HasSegments() bool { return s.flushed && len(s.pcm) > 0 }
func (s *playbackTestSegmenter) NextSegment() ([]float32, bool) {
	if !s.HasSegments() {
		return nil, false
	}
	p := s.pcm
	s.pcm = nil
	return p, true
}
func (s *playbackTestSegmenter) Reset() { s.pcm = nil; s.speech = false; s.flushed = false }
func (s *playbackTestSegmenter) Flush() { s.flushed = true }

type playbackTestRecognizer struct{ pcm []float32 }

func (r *playbackTestRecognizer) Accept(p []float32) { r.pcm = append(r.pcm, p...) }
func (*playbackTestRecognizer) Partial() string      { return "" }
func (*playbackTestRecognizer) IsEndpoint() bool     { return false }
func (*playbackTestRecognizer) Finalize() string     { return "retained" }
func (r *playbackTestRecognizer) Reset() error       { r.pcm = nil; return nil }

func completionTestFrame(offset uint64, at time.Time, value float32) audio.Frame {
	return audio.Frame{Samples: constFrame(1600, value), SampleRate: audio.SampleRate, Channels: 1, SourceKind: audio.SourceLive,
		CaptureSpans: []audio.CaptureSpan{{SampleOffset: offset, SampleCount: 1600, DeviceGeneration: 1, EstimatedStart: at, CallbackAt: at.Add(100 * time.Millisecond)}}}
}

type playbackLoopHarness struct {
	ctx     context.Context
	cancel  context.CancelFunc
	session *sherpaSession
	frames  *playbackTestFrames
	token   PauseToken
	attempt audio.PlaybackAttempt
	done    chan struct{}
	events  chan Event
	decoded []float32
	rec     *playbackTestRecognizer
}

func newPlaybackLoopHarness(t *testing.T, streaming bool) *playbackLoopHarness {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	h := &playbackLoopHarness{ctx: ctx, cancel: cancel, frames: &playbackTestFrames{reads: make(chan audio.Frame, 32)}, done: make(chan struct{}), events: make(chan Event, 64), rec: &playbackTestRecognizer{}}
	scope := work.WorkScope{SessionID: "test", RuntimeEpoch: "r", ConversationEpoch: "c"}
	p := newPauseController("test")
	p.BindCompletionCoordinator(work.NewCoordinator(scope))
	h.session = &sherpaSession{pause: p, playback: true}
	seg := &playbackTestSegmenter{}
	go func() {
		defer close(h.done)
		if streaming {
			runStreamingLoop(ctx, streamingLoopDeps{frames: h.frames, seg: seg, rec: h.rec, pause: p, policy: endpoint.Policy{FinalizeOnEOF: true}}, h.events)
		} else {
			runOfflineLoop(ctx, offlineLoopDeps{frames: h.frames, seg: seg, pause: p, transcribe: func(pcm []float32) (string, error) { h.decoded = slices.Clone(pcm); return "retained", nil }}, h.events)
		}
	}()
	t.Cleanup(func() { cancel(); <-h.done })
	result, err := p.request(ctx, scope, "pause")
	if err != nil {
		t.Fatal(err)
	}
	h.token = result.Token
	h.attempt = audio.PlaybackAttempt{ID: "attempt", CaptureEpoch: h.token.Permit.CaptureEpoch, Render: &audio.FirstRenderBoundary{}}
	if err := h.session.BindCompletionPlayback(ctx, h.token, h.attempt, 300*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	return h
}
func (h *playbackLoopHarness) waitRead(t *testing.T) {
	t.Helper()
	select {
	case <-h.frames.reads:
	case <-h.ctx.Done():
		t.Fatal("capture read timed out")
	}
}
func (h *playbackLoopHarness) waitDone(t *testing.T) []Event {
	t.Helper()
	select {
	case <-h.done:
	case <-time.After(time.Second):
		t.Fatal("STT did not finish")
	}
	var events []Event
	for {
		select {
		case event := <-h.events:
			events = append(events, event)
		default:
			return events
		}
	}
}
func (h *playbackLoopHarness) commandBarrier(t *testing.T) {
	t.Helper()
	if err := h.session.ConfirmCompletionPlayback(h.ctx, h.token, "stale"); err == nil {
		t.Fatal("stale attempt accepted")
	}
}

func TestCompletionPlaybackQualifiedOrNearMissRetainsExactPCM(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		for _, nearMiss := range []bool{false, true} {
			t.Run(map[bool]string{false: "offline/", true: "streaming/"}[streaming]+map[bool]string{false: "qualified", true: "near_miss"}[nearMiss], func(t *testing.T) {
				h := newPlaybackLoopHarness(t, streaming)
				at := time.Now()
				h.attempt.Render.Observe(at)
				first := completionTestFrame(0, at.Add(time.Second), .1)
				second := completionTestFrame(1600, at.Add(1100*time.Millisecond), .2)
				third := completionTestFrame(3200, at.Add(1200*time.Millisecond), .3)
				h.frames.push(first)
				h.waitRead(t)
				h.commandBarrier(t)
				if h.session.HasCapturedInput() || !h.token.Permit.Valid() {
					t.Fatal("parked playback independently revoked capture")
				}
				h.frames.push(second, third, audio.Frame{Final: true})
				h.commandBarrier(t)
				select {
				case <-h.frames.reads:
					t.Fatal("parked loop drained ring")
				default:
				}
				var err error
				if nearMiss {
					err = h.session.FinishCompletionPlayback(h.ctx, h.token, h.attempt.ID, true)
				} else {
					err = h.session.ConfirmCompletionPlayback(h.ctx, h.token, h.attempt.ID)
				}
				if err != nil {
					t.Fatal(err)
				}
				got := h.waitDone(t)
				if _, ok := terminal(got).(FinalTranscript); !ok {
					t.Fatalf("terminal=%#v", terminal(got))
				}
				want := append(append(slices.Clone(first.Samples), second.Samples...), third.Samples...)
				actual := h.decoded
				if streaming {
					actual = h.rec.pcm
				}
				if !slices.Equal(actual, want) {
					t.Fatalf("retained PCM changed/duplicated: got%d want%d", len(actual), len(want))
				}
				// The decoder can finish before response synthesis has joined.
				if err := h.session.FinishCompletionPlayback(h.ctx, h.token, h.attempt.ID, false); err != nil {
					t.Fatalf("finish after terminal: %v", err)
				}
				if err := h.session.ResumeCapture(h.ctx, h.token); err != nil {
					t.Fatalf("resume terminal retained session: %v", err)
				}
			})
		}
	}
}

func TestCompletionPlaybackUnacceptedResetsBeforeNextTranscript(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		t.Run(map[bool]string{false: "offline", true: "streaming"}[streaming], func(t *testing.T) {
			h := newPlaybackLoopHarness(t, streaming)
			at := time.Now()
			h.attempt.Render.Observe(at)
			h.frames.push(completionTestFrame(0, at.Add(time.Second), .5))
			h.waitRead(t)
			h.commandBarrier(t)
			// A long unaccepted response may overflow the ordinary bounded ring.
			// Its historical loss is retained but is not a failed user utterance.
			h.frames.retainResult = audio.Frame{CaptureGap: audio.CaptureGap{OverflowSamples: 60000, DiscardedSamples: 1600, Reset: true, DeviceDiscontinuity: true}}
			if err := h.session.FinishCompletionPlayback(h.ctx, h.token, h.attempt.ID, false); err != nil {
				t.Fatal(err)
			}
			if h.session.HasCapturedInput() {
				t.Fatal("unqualified playback seeded captured input")
			}
			if err := h.session.ResumeCapture(h.ctx, h.token); err != nil {
				t.Fatal(err)
			}
			var want []float32
			for i := range 3 {
				frame := completionTestFrame(uint64(64000+i*1600), at.Add(time.Duration(10+i)*time.Second), .1)
				want = append(want, frame.Samples...)
				h.frames.push(frame)
			}
			h.frames.push(audio.Frame{Final: true})
			got := h.waitDone(t)
			if _, ok := terminal(got).(FinalTranscript); !ok {
				t.Fatalf("terminal=%#v", terminal(got))
			}
			actual := h.decoded
			if streaming {
				actual = h.rec.pcm
			}
			if !slices.Equal(actual, want) {
				t.Fatalf("unaccepted playback leaked into next input: %d/%d", len(actual), len(want))
			}
			if h.frames.retentions != 1 {
				t.Fatalf("retention count%d", h.frames.retentions)
			}
		})
	}
}

func TestCompletionPlaybackPreRenderStraddlingAndUnknownRemainOrdinary(t *testing.T) {
	for _, kind := range []string{"pre_render", "straddling", "unknown", "never_rendered"} {
		t.Run(kind, func(t *testing.T) {
			h := newPlaybackLoopHarness(t, false)
			at := time.Now()
			if kind != "never_rendered" {
				h.attempt.Render.Observe(at)
			}
			first := completionTestFrame(0, at.Add(-time.Second), .1)
			if kind == "straddling" {
				first = completionTestFrame(0, at.Add(-50*time.Millisecond), .1)
			}
			if kind == "unknown" {
				first.CaptureSpans = nil
			}
			second := completionTestFrame(1600, at.Add(time.Second), .2)
			third := completionTestFrame(3200, at.Add(2*time.Second), .3)
			h.frames.push(first, second, third, audio.Frame{Final: true})
			got := h.waitDone(t)
			if _, ok := terminal(got).(FinalTranscript); !ok {
				t.Fatalf("terminal=%#v", terminal(got))
			}
			if h.token.Permit.Valid() || !h.session.HasCapturedInput() {
				t.Fatal("ordinary pre-render speech did not revoke and retain")
			}
			want := append(append(slices.Clone(first.Samples), second.Samples...), third.Samples...)
			if !slices.Equal(h.decoded, want) {
				t.Fatal("pre-render onset lost")
			}
			if err := h.session.FinishCompletionPlayback(h.ctx, h.token, h.attempt.ID, false); err != nil {
				t.Fatal(err)
			}
			if h.frames.retentions != 0 {
				t.Fatal("pre-render input was trimmed")
			}
		})
	}
}

func TestCompletionPlaybackAcceptedCaptureLossFails(t *testing.T) {
	for _, kind := range []string{"overflow", "gap", "generation", "unknown"} {
		t.Run(kind, func(t *testing.T) {
			h := newPlaybackLoopHarness(t, false)
			at := time.Now()
			h.attempt.Render.Observe(at)
			h.frames.push(completionTestFrame(0, at.Add(time.Second), .1))
			h.waitRead(t)
			h.commandBarrier(t)
			second := completionTestFrame(1600, at.Add(2*time.Second), .2)
			switch kind {
			case "overflow":
				second.CaptureGap.OverflowSamples = 960
			case "gap":
				second.CaptureSpans[0].SampleOffset++
			case "generation":
				second.CaptureSpans[0].DeviceGeneration = 2
			case "unknown":
				second.CaptureSpans = nil
			}
			h.frames.push(second, audio.Frame{Final: true})
			if err := h.session.ConfirmCompletionPlayback(h.ctx, h.token, h.attempt.ID); err != nil {
				t.Fatal(err)
			}
			got := h.waitDone(t)
			if _, ok := terminal(got).(Failure); !ok {
				t.Fatalf("loss terminal=%#v", terminal(got))
			}
			if len(h.decoded) != 0 {
				t.Fatal("decoded discontinuous capture")
			}
		})
	}
}

func TestCompletionPlaybackCommandsRejectStaleAndHonorCancellation(t *testing.T) {
	h := newPlaybackLoopHarness(t, false)
	at := time.Now()
	h.attempt.Render.Observe(at)
	h.frames.push(completionTestFrame(0, at.Add(time.Second), .1))
	h.waitRead(t)
	h.commandBarrier(t)
	stale := h.token
	stale.Epoch++
	if err := h.session.ConfirmCompletionPlayback(h.ctx, stale, h.attempt.ID); err == nil {
		t.Fatal("stale token accepted")
	}
	if err := h.session.FinishCompletionPlayback(h.ctx, h.token, "other", false); err == nil {
		t.Fatal("stale finish accepted")
	}
	ctx, cancel := context.WithCancel(h.ctx)
	cancel()
	if err := h.session.ConfirmCompletionPlayback(ctx, h.token, h.attempt.ID); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled confirm=%v", err)
	}
	if h.session.HasCapturedInput() {
		t.Fatal("invalid confirmation changed ownership")
	}
	h.cancel()
	got := h.waitDone(t)
	if fail, ok := terminal(got).(Failure); !ok || !errors.Is(fail.Err, context.Canceled) {
		t.Fatalf("parked cancel=%#v", terminal(got))
	}
}

type playbackBindBarrier struct {
	*playbackTestFrames
	entered, release chan struct{}
}

func (f *playbackBindBarrier) CanRetainForNextListen() bool {
	close(f.entered)
	<-f.release
	return true
}

func TestCompletionPlaybackAppliedBindWinsCancellationBeforeAcknowledgement(t *testing.T) {
	owner := newPauseController("test")
	token := PauseToken{RequestID: "pause", Epoch: 1}
	owner.token, owner.paused = token, true
	source := &playbackBindBarrier{playbackTestFrames: &playbackTestFrames{reads: make(chan audio.Frame, 1)}, entered: make(chan struct{}), release: make(chan struct{})}
	reader := completionFrameReader{owner: owner, frames: source}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	command := &playbackCommand{kind: playbackBind, token: token, attempt: audio.PlaybackAttempt{ID: "a", Render: &audio.FirstRenderBoundary{}}}
	result := make(chan error, 1)
	go func() { result <- owner.playbackCommand(ctx, command) }()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for reader.binding == nil {
			reader.read(t.Context(), false, func() error { return nil })
		}
	}()
	select {
	case <-source.entered:
	case <-time.After(time.Second):
		t.Fatal("binding never began")
	}
	cancel()
	select {
	case err := <-result:
		t.Fatalf("applying bind returned ambiguous cancellation: %v", err)
	default:
	}
	close(source.release)
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("applied bind result=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("bind acknowledgement lost")
	}
	<-done
	if !owner.playbackBound || reader.binding == nil {
		t.Fatal("successful acknowledgement missing binding")
	}
}

func TestCompletionPlaybackCanceledPendingCommandCannotBindLater(t *testing.T) {
	owner := newPauseController("test")
	ctx, cancel := context.WithCancel(t.Context())
	command := &playbackCommand{kind: playbackBind}
	result := make(chan error, 1)
	go func() { result <- owner.playbackCommand(ctx, command) }()
	var queued *playbackCommand
	select {
	case queued = <-owner.playbackCommands:
	case <-time.After(time.Second):
		t.Fatal("command not queued")
	}
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("pending cancellation=%v", err)
	}
	reader := completionFrameReader{owner: owner, frames: noFrames{}}
	reader.apply(queued, func() error { return nil })
	if reader.binding != nil || owner.playbackBound {
		t.Fatal("canceled pending command later installed binding")
	}
}

func TestCompletionPlaybackCapabilityAndRetainedGap(t *testing.T) {
	owner := newPauseController("test")
	token := PauseToken{RequestID: "pause"}
	owner.token, owner.paused = token, true
	attempt := audio.PlaybackAttempt{ID: "a", Render: &audio.FirstRenderBoundary{}}
	command := func(kind playbackCommandKind) *playbackCommand {
		return &playbackCommand{ctx: t.Context(), kind: kind, token: token, attempt: attempt}
	}
	reader := completionFrameReader{owner: owner, frames: audio.NewFinalizable(noFrames{})}
	if err := reader.command(command(playbackBind), func() error { return nil }); !errors.Is(err, ErrCompletionPlaybackUnsupported) {
		t.Fatalf("unsupported bind=%v", err)
	}
	at := time.Now()
	parked := completionTestFrame(0, at, .1)
	source := &playbackTestFrames{reads: make(chan audio.Frame, 2), retainResult: audio.Frame{CaptureGap: audio.CaptureGap{Reset: true, DiscardedSamples: 1600, OverflowSamples: 500, DeviceDiscontinuity: true}}}
	reader.frames = source
	if err := reader.command(command(playbackBind), func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	reader.parked = &parked
	resetCalled := false
	if err := reader.command(command(playbackFinish), func() error {
		resetCalled = true
		if !owner.mu.TryLock() {
			t.Error("native reset runs under pause mutex")
		} else {
			owner.mu.Unlock()
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !resetCalled {
		t.Fatal("unaccepted decoder state not reset")
	}
	source.push(completionTestFrame(2100, at.Add(time.Second), .2))
	read := reader.read(t.Context(), false, func() error { return nil })
	want := source.retainResult.CaptureGap
	if read.err != nil || read.frame.CaptureGap != want {
		t.Fatalf("empty retained frame lost source gaps: got%+v want%+v err%v", read.frame.CaptureGap, want, read.err)
	}
	source.push(completionTestFrame(3700, at.Add(2*time.Second), .2))
	read = reader.read(t.Context(), false, func() error { return nil })
	if read.frame.CaptureGap != (audio.CaptureGap{}) {
		t.Fatal("retained source gap duplicated")
	}
}
