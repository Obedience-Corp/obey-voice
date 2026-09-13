package stt

import (
	"context"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/endpoint"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/work"
)

type barrierFrames struct{ in chan audio.Frame }

func (f *barrierFrames) ReadFrame(ctx context.Context) (audio.Frame, error) {
	select {
	case frame := <-f.in:
		return frame, nil
	case <-ctx.Done():
		return audio.Frame{}, ctx.Err()
	case <-time.After(time.Millisecond):
		return audio.Frame{}, audio.ErrNoFrameReady
	}
}
func (*barrierFrames) Close() error { return nil }

func TestOfflinePauseYieldsAtFrameBoundaryAndRevokesOnSpeech(t *testing.T) {
	scope := work.WorkScope{SessionID: "s", RuntimeEpoch: "r", ConversationEpoch: "c"}
	coord := work.NewCoordinator(scope)
	ctrl := newPauseController("test-stt")
	ctrl.BindCompletionCoordinator(coord)
	frames := &barrierFrames{in: make(chan audio.Frame)}
	seg := &fakeSegmenter{speechSeq: []bool{false, true}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events := make(chan Event, 16)
	done := make(chan struct{})
	go func() {
		runOfflineLoop(ctx, offlineLoopDeps{
			frames: frames, seg: seg, pause: ctrl,
			policy:     endpoint.Policy{},
			transcribe: func([]float32) (string, error) { return "", nil },
		}, events)
		close(done)
	}()

	frames.in <- audio.Frame{Samples: make([]float32, audio.ChunkSize), SourceKind: audio.SourceLive}
	requestCtx, requestCancel := context.WithTimeout(context.Background(), time.Second)
	result, err := ctrl.request(requestCtx, scope, "request-1")
	requestCancel()
	if err != nil || result.Decision != PauseAcknowledged || !result.Token.Permit.Valid() {
		t.Fatalf("pause = %+v, %v", result, err)
	}
	eventDeadline := time.After(time.Second)
	for {
		select {
		case event := <-events:
			yielded, ok := event.(YieldedForCompletion)
			if !ok {
				continue
			}
			if yielded.RequestID != "request-1" || yielded.Token.Permit.PermitID == "" {
				t.Fatalf("yield event = %#v", event)
			}
			goto gotYield
		case <-eventDeadline:
			t.Fatal("timed out waiting for yielded event")
		}
	}

gotYield:

	speech := make([]float32, audio.ChunkSize)
	for i := range speech {
		speech[i] = 0.5
	}
	frames.in <- audio.Frame{Samples: speech, SourceKind: audio.SourceLive}
	deadline := time.After(time.Second)
	for !ctrl.hasCapturedInput() {
		select {
		case <-deadline:
			t.Fatal("speech did not revoke completion permit")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	if err := ctrl.resume(context.Background(), result.Token); err != nil {
		t.Fatalf("resume revoked token: %v", err)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("offline loop did not stop")
	}
}

func TestPauseResumeEmitsListeningReadinessForBothLoops(t *testing.T) {
	for _, tc := range []struct {
		name string
		run  func(context.Context, *pauseController, chan<- Event)
	}{
		{name: "offline", run: func(ctx context.Context, p *pauseController, out chan<- Event) {
			runOfflineLoop(ctx, offlineLoopDeps{frames: noFrames{}, seg: &fakeSegmenter{}, pause: p, policy: endpoint.Policy{StartTimeout: time.Second}, transcribe: func([]float32) (string, error) { return "", nil }}, out)
		}},
		{name: "streaming", run: func(ctx context.Context, p *pauseController, out chan<- Event) {
			runStreamingLoop(ctx, streamingLoopDeps{frames: noFrames{}, seg: &fakeSegmenter{}, rec: &fakeStreamingRec{}, pause: p, policy: endpoint.Policy{StartTimeout: time.Second}}, out)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			scope := pauseTestScope()
			pause := newPauseController("ready-" + tc.name)
			pause.BindCompletionCoordinator(work.NewCoordinator(scope))
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			out := make(chan Event, 32)
			done := make(chan struct{})
			go func() { tc.run(ctx, pause, out); close(done) }()
			requestCtx, requestCancel := context.WithTimeout(ctx, time.Second)
			result, err := pause.request(requestCtx, scope, "ready")
			requestCancel()
			if err != nil || result.Decision != PauseAcknowledged {
				t.Fatalf("pause = %+v, %v", result, err)
			}
			deadline := time.After(time.Second)
			for {
				select {
				case event := <-out:
					if yielded, ok := event.(YieldedForCompletion); ok && yielded.RequestID == "ready" {
						if err := pause.resume(context.Background(), result.Token); err != nil {
							t.Fatal(err)
						}
						goto resumed
					}
				case <-deadline:
					t.Fatal("timed out waiting for yielded event")
				}
			}
		resumed:
			deadline = time.After(time.Second)
			for {
				select {
				case event := <-out:
					if phase, ok := event.(PhaseEvent); ok && phase.Phase == "listening" {
						cancel()
						<-done
						return
					}
				case <-deadline:
					t.Fatal("timed out waiting for resumed listening phase")
				}
			}
		})
	}
}
