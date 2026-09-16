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

// Hold one empty source poll while playback finishes and capture accumulates
// unread PCM. The finish command is then applied before any frame can park.
type finishBeforeParkFrames struct {
	*playbackTestFrames
	once         sync.Once
	polling      chan struct{}
	release      chan struct{}
	retainedFrom audio.Frame
	retainedFor  time.Duration
}

func (f *finishBeforeParkFrames) ReadFrame(ctx context.Context) (audio.Frame, error) {
	first := false
	f.once.Do(func() { first = true })
	if first {
		close(f.polling)
		select {
		case <-f.release:
			return audio.Frame{}, audio.ErrNoFrameReady
		case <-ctx.Done():
			return audio.Frame{}, ctx.Err()
		}
	}
	return f.playbackTestFrames.ReadFrame(ctx)
}

func (f *finishBeforeParkFrames) RetainForNextListen(parked audio.Frame, keep time.Duration) (audio.Frame, error) {
	f.retainedFrom, f.retainedFor = parked, keep
	return f.playbackTestFrames.RetainForNextListen(parked, keep)
}

func TestCompletionPlaybackFinishBeforeFirstPark(t *testing.T) {
	retentionErr := errors.New("capture retention failed")
	for _, streaming := range []bool{false, true} {
		for _, tc := range []struct {
			name      string
			rendered  bool
			admitted  bool
			retainErr error
		}{
			{name: "rendered_unaccepted", rendered: true},
			{name: "never_rendered"},
			{name: "admitted", rendered: true, admitted: true},
			{name: "retention_failure", rendered: true, retainErr: retentionErr},
		} {
			t.Run(map[bool]string{false: "offline/", true: "streaming/"}[streaming]+tc.name, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
				defer cancel()
				scope := work.WorkScope{SessionID: "finish", RuntimeEpoch: "r", ConversationEpoch: "c"}
				coord := work.NewCoordinator(scope)
				permit, err := coord.AcquireCapturePermit(scope, "test", 1)
				if err != nil {
					t.Fatal(err)
				}
				token := PauseToken{Scope: scope, RequestID: "pause", Epoch: 1, Permit: permit}
				owner := newPauseController("test")
				owner.coord, owner.token, owner.permit, owner.paused = coord, token, permit, true
				attempt := audio.PlaybackAttempt{ID: "attempt", CaptureEpoch: permit.CaptureEpoch, Render: &audio.FirstRenderBoundary{}}
				at := time.Now()
				if tc.rendered {
					attempt.Render.Observe(at)
				}
				source := &finishBeforeParkFrames{
					playbackTestFrames: &playbackTestFrames{reads: make(chan audio.Frame, 32), retainErr: tc.retainErr},
					polling:            make(chan struct{}), release: make(chan struct{}),
				}
				bind := &playbackCommand{kind: playbackBind, ctx: ctx, token: token, attempt: attempt, preRoll: 300 * time.Millisecond, result: make(chan error, 1)}
				owner.playbackCommands <- bind
				events := make(chan Event, 64)
				done := make(chan struct{})
				seg := &playbackTestSegmenter{}
				rec := &playbackTestRecognizer{}
				var decoded []float32
				go func() {
					defer close(done)
					if streaming {
						runStreamingLoop(ctx, streamingLoopDeps{frames: source, seg: seg, rec: rec, pause: owner, policy: endpoint.Policy{FinalizeOnEOF: true}}, events)
					} else {
						runOfflineLoop(ctx, offlineLoopDeps{frames: source, seg: seg, pause: owner, transcribe: func(pcm []float32) (string, error) {
							decoded = slices.Clone(pcm)
							return "retained", nil
						}}, events)
					}
				}()
				t.Cleanup(func() { cancel(); <-done })
				select {
				case err := <-bind.result:
					if err != nil {
						t.Fatal(err)
					}
				case <-ctx.Done():
					t.Fatal("bind did not complete")
				}
				select {
				case <-source.polling:
				case <-ctx.Done():
					t.Fatal("source poll did not start")
				}
				var oldPCM []float32
				for i := range 3 {
					frame := completionTestFrame(uint64(i*1600), at.Add(time.Duration(i+1)*time.Second), .5+float32(i)*.1)
					oldPCM = append(oldPCM, frame.Samples...)
					source.push(frame)
				}
				// The source retains its newest 300ms, not an empty buffer. This
				// tail remains ordinary pre-roll; only the older playback prefix
				// must disappear when the completion was never admitted.
				tail := completionTestFrame(4800, at.Add(4*time.Second), .25)
				tail.Samples = constFrame(4800, .25)
				tail.CaptureSpans[0].SampleCount = len(tail.Samples)
				tail.CaptureSpans[0].CallbackAt = tail.CaptureSpans[0].EstimatedStart.Add(300 * time.Millisecond)
				source.retainResult = tail
				source.push(tail)
				finish := &playbackCommand{kind: playbackFinish, ctx: ctx, token: token, attempt: attempt, retain: tc.admitted, result: make(chan error, 1)}
				owner.playbackCommands <- finish
				close(source.release)
				select {
				case err := <-finish.result:
					if !errors.Is(err, tc.retainErr) {
						t.Fatalf("finish error = %v, want %v", err, tc.retainErr)
					}
				case <-ctx.Done():
					t.Fatal("finish did not complete")
				}
				var want []float32
				if !tc.rendered || tc.admitted {
					want = append(want, oldPCM...)
				}
				want = append(want, tail.Samples...)
				for i := range 3 {
					frame := completionTestFrame(uint64((i+6)*1600), at.Add(time.Duration(i+5)*time.Second), .1+float32(i)*.1)
					want = append(want, frame.Samples...)
					source.push(frame)
				}
				source.push(audio.Frame{Final: true})
				select {
				case <-done:
				case <-ctx.Done():
					t.Fatal("STT loop did not finish")
				}
				close(events)
				var gotEvents []Event
				for event := range events {
					gotEvents = append(gotEvents, event)
				}
				actual := decoded
				if streaming {
					actual = rec.pcm
				}
				if tc.retainErr != nil {
					failure, ok := terminal(gotEvents).(Failure)
					if !ok || !errors.Is(failure.Err, tc.retainErr) || len(actual) != 0 {
						t.Fatalf("retention failure did not fail closed: terminal=%#v decoded=%d", terminal(gotEvents), len(actual))
					}
				} else if _, ok := terminal(gotEvents).(FinalTranscript); !ok || !slices.Equal(actual, want) {
					t.Fatalf("finish input mismatch: terminal=%#v decoded=%d want=%d", terminal(gotEvents), len(actual), len(want))
				}
				wantRetentions := 0
				if tc.rendered && !tc.admitted {
					wantRetentions = 1
				}
				if source.retentions != wantRetentions {
					t.Fatalf("retention calls = %d, want %d", source.retentions, wantRetentions)
				}
				if wantRetentions != 0 && (len(source.retainedFrom.Samples) != 0 || source.retainedFor != 300*time.Millisecond) {
					t.Fatalf("retention did not receive empty parked frame and configured pre-roll: %+v, %s", source.retainedFrom, source.retainedFor)
				}
			})
		}
	}
}
