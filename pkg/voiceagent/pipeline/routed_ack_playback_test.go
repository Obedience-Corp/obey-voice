package pipeline

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/job"
)

// gatedRouteBrain holds the model open until released, then routes to a worker.
type gatedRouteBrain struct {
	fakeBrain
	entered chan struct{}
	release chan struct{}
}

func (b *gatedRouteBrain) ThinkStream(ctx context.Context, _ string, _ brain.StreamOptions) (*brain.Stream, error) {
	out := make(chan string)
	done := make(chan brain.StreamResult, 1)
	go func() {
		defer close(out)
		defer close(done)
		close(b.entered)
		select {
		case <-b.release:
			done <- brain.StreamResult{Route: "spawn"}
		case <-ctx.Done():
			done <- brain.StreamResult{Err: ctx.Err()}
		}
	}()
	return &brain.Stream{Chunks: out, Done: done}, nil
}

func TestBrainRoutedSpawnSpeaksAcknowledgementAfterStoppingTurnSpeech(t *testing.T) {
	for _, userStopped := range []bool{false, true} {
		t.Run(map[bool]string{false: "routed", true: "user stopped speech first"}[userStopped], func(t *testing.T) {
			engine := &stopGenerationEngine{&generationTestEngine{fakePlayer: newFakePlayer(time.Millisecond)}}
			defer engine.Close()
			provider := &progressiveFakeTTS{}
			routed := &gatedRouteBrain{entered: make(chan struct{}), release: make(chan struct{})}
			p := &Pipeline{
				STT:    &fakeSTT{text: "look through the repo and summarize it"},
				Brain:  routed,
				TTS:    provider,
				Player: engine,
				Events: events.NewBus(),
			}
			var mu sync.Mutex
			var failures []events.Error
			events.Subscribe(p.Events, func(e events.Error) {
				mu.Lock()
				failures = append(failures, e)
				mu.Unlock()
			})
			worker := &slowWorker{started: make(chan struct{}), done: make(chan struct{}), reply: "plain result"}
			defer close(worker.done)
			jobs := job.NewTable(t.Context())
			t.Cleanup(jobs.Close)
			NewAgency(p, jobs, worker, &config.Config{}, nil).Wire()

			turnErr := make(chan error, 1)
			go func() {
				_, err := p.RunTurn(t.Context())
				turnErr <- err
			}()
			select {
			case <-routed.entered:
			case <-time.After(2 * time.Second):
				t.Fatal("model was not called")
			}
			if userStopped {
				p.StopPlayback()
			}
			close(routed.release)
			select {
			case err := <-turnErr:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("routed turn did not finish")
			}
			select {
			case <-worker.started:
			case <-time.After(2 * time.Second):
				t.Fatal("worker did not start")
			}

			engine.mu.Lock()
			accepted := engine.accepted
			engine.mu.Unlock()
			mu.Lock()
			defer mu.Unlock()
			if len(failures) != 0 {
				t.Fatalf("routed acknowledgement reported failures: %+v", failures)
			}
			if userStopped {
				if accepted != 0 {
					t.Fatalf("acknowledgement played after the user stopped speech: %d", accepted)
				}
				return
			}
			if accepted != 1 {
				t.Fatalf("acknowledgement playback admitted %d times, want 1 (synthesized %v)", accepted, provider.Calls())
			}
		})
	}
}
