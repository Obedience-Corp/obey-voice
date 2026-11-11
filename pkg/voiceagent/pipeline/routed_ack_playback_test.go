package pipeline

import (
	"context"
	"strings"
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
	spoken  string
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
		if b.spoken != "" {
			select {
			case out <- b.spoken:
			case <-ctx.Done():
				done <- brain.StreamResult{Err: ctx.Err()}
				return
			}
		}
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
			var refused bool
			events.Subscribe(p.Events, func(e events.Error) {
				mu.Lock()
				failures = append(failures, e)
				mu.Unlock()
			})
			events.Subscribe(p.Events, func(e events.Info) {
				mu.Lock()
				refused = refused || e.Message == "speech not played: output was stopped"
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
				if !refused {
					t.Fatal("refused acknowledgement left no trace")
				}
				return
			}
			if accepted != 1 {
				t.Fatalf("acknowledgement playback admitted %d times, want 1 (synthesized %v)", accepted, provider.Calls())
			}
		})
	}
}

const routedAck = "I'll work on that and report back."

type routedAckRig struct {
	engine   *stopGenerationEngine
	provider *progressiveFakeTTS
	brain    *gatedRouteBrain
	p        *Pipeline
	worker   *slowWorker
	failures func() []events.Error
	infos    func() []events.Info
}

func newRoutedAckRig(t *testing.T, spoken string) *routedAckRig {
	t.Helper()
	engine := &stopGenerationEngine{&generationTestEngine{fakePlayer: newFakePlayer(time.Second)}}
	t.Cleanup(func() { engine.Close() })
	routed := &gatedRouteBrain{spoken: spoken, entered: make(chan struct{}), release: make(chan struct{})}
	p := &Pipeline{
		STT:    &fakeSTT{text: "look through the repo and summarize it"},
		Brain:  routed,
		TTS:    &progressiveFakeTTS{},
		Player: engine,
		Events: events.NewBus(),
	}
	var mu sync.Mutex
	var failures []events.Error
	var infos []events.Info
	events.Subscribe(p.Events, func(e events.Error) {
		mu.Lock()
		failures = append(failures, e)
		mu.Unlock()
	})
	events.Subscribe(p.Events, func(e events.Info) {
		mu.Lock()
		infos = append(infos, e)
		mu.Unlock()
	})
	worker := &slowWorker{started: make(chan struct{}), done: make(chan struct{}), reply: "plain result"}
	t.Cleanup(func() { close(worker.done) })
	jobs := job.NewTable(t.Context())
	t.Cleanup(jobs.Close)
	NewAgency(p, jobs, worker, &config.Config{}, nil).Wire()
	return &routedAckRig{
		engine: engine, provider: p.TTS.(*progressiveFakeTTS), brain: routed, p: p, worker: worker,
		failures: func() []events.Error {
			mu.Lock()
			defer mu.Unlock()
			return append([]events.Error(nil), failures...)
		},
		infos: func() []events.Info {
			mu.Lock()
			defer mu.Unlock()
			return append([]events.Info(nil), infos...)
		},
	}
}

func (r *routedAckRig) acceptedCount() int {
	r.engine.mu.Lock()
	defer r.engine.mu.Unlock()
	return r.engine.accepted
}

func (r *routedAckRig) waitAccepted(t *testing.T, n int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for r.acceptedCount() < n {
		if time.Now().After(deadline) {
			t.Fatalf("playback admitted %d times, want %d (synthesized %v)", r.acceptedCount(), n, r.provider.Calls())
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func (r *routedAckRig) synthesized(text string) bool {
	for _, call := range r.provider.Calls() {
		if strings.Contains(call, text) {
			return true
		}
	}
	return false
}

func TestBrainRoutedSpawnSpeaksAcknowledgementAfterSpokenSentence(t *testing.T) {
	const sentence = "Let me look into that for you right now."
	r := newRoutedAckRig(t, sentence+" ")
	turnErr := make(chan error, 1)
	go func() {
		_, err := r.p.RunTurn(t.Context())
		turnErr <- err
	}()
	select {
	case <-r.brain.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("model was not called")
	}
	r.waitAccepted(t, 1)
	if r.synthesized(routedAck) {
		t.Fatal("acknowledgement synthesized before the route arrived")
	}
	close(r.brain.release)
	select {
	case err := <-turnErr:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("routed turn did not finish")
	}
	select {
	case <-r.worker.started:
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not start")
	}
	r.waitAccepted(t, 2)
	if !r.synthesized(routedAck) {
		t.Fatalf("acknowledgement not synthesized: %v", r.provider.Calls())
	}
	if f := r.failures(); len(f) != 0 {
		t.Fatalf("routed acknowledgement reported failures: %+v", f)
	}
}

func TestTextModeRoutedSpawnSpeaksAcknowledgement(t *testing.T) {
	r := newRoutedAckRig(t, "")
	turnErr := make(chan error, 1)
	go func() { turnErr <- r.p.RunTurnTextMode(t.Context(), "look through the repo and summarize it") }()
	select {
	case <-r.brain.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("model was not called")
	}
	close(r.brain.release)
	select {
	case err := <-turnErr:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("routed text turn did not finish")
	}
	r.waitAccepted(t, 1)
	if !r.synthesized(routedAck) {
		t.Fatalf("acknowledgement not synthesized: %v", r.provider.Calls())
	}
	if f := r.failures(); len(f) != 0 {
		t.Fatalf("routed acknowledgement reported failures: %+v", f)
	}
}
