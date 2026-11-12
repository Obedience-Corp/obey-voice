package brain

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

// heldCodex holds each request until the test releases it.
type heldCodex struct {
	mu       sync.Mutex
	requests []codexRequest
	started  chan struct{}
	release  chan struct{}
}

func newHeldCodex() *heldCodex {
	return &heldCodex{started: make(chan struct{}, 8), release: make(chan struct{}, 8)}
}

func (f *heldCodex) run(ctx context.Context, r codexRequest, emit func(string) error, _ StreamOptions) (codexResult, error) {
	f.mu.Lock()
	f.requests = append(f.requests, r)
	n := len(f.requests)
	f.mu.Unlock()
	f.started <- struct{}{}
	select {
	case <-f.release:
	case <-ctx.Done():
		return codexResult{}, ctx.Err()
	}
	if err := emit("Noted."); err != nil {
		return codexResult{}, err
	}
	return codexResult{ThreadID: fmt.Sprintf("thread-%d", n)}, nil
}

func (f *heldCodex) request(t *testing.T, i int) codexRequest {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if i >= len(f.requests) {
		t.Fatalf("codex requests = %d, want more than %d", len(f.requests), i)
	}
	return f.requests[i]
}

func TestCodexJobResultRecordedWhileARequestRunsReachesTheNextRequestOnce(t *testing.T) {
	f := newHeldCodex()
	c := &CodexBrain{client: f, cfg: &config.Config{AgentName: "Obey", ActivePersona: "helper", MaxHistory: 20}, systemPrompt: "Persona fixture", turnInstruction: "Speak briefly."}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	f.release <- struct{}{}
	if _, err := c.ThinkFull(ctx, "Remember cobalt.", StreamOptions{}); err != nil {
		t.Fatal(err)
	}
	<-f.started

	stream, err := c.ThinkStream(ctx, "What next?", StreamOptions{})
	if err != nil {
		t.Fatal(err)
	}
	<-f.started
	recorded := make(chan error, 1)
	go func() {
		recorded <- c.AppendAssistantResponseWithIdentity("Job result: the report is ready.", TurnIdentity{PersonaID: "helper", PersonaDisplayName: "Helper"})
	}()
	select {
	case err := <-recorded:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("recording a job result waited for the request in flight")
	}
	f.release <- struct{}{}
	if _, res := collectStream(t, stream); res.Err != nil {
		t.Fatal(res.Err)
	}

	f.release <- struct{}{}
	if _, err := c.ThinkFull(ctx, "Anything new?", StreamOptions{}); err != nil {
		t.Fatal(err)
	}
	next := f.request(t, 2)
	if got := strings.Count(next.Prompt, "the report is ready."); got != 1 {
		t.Fatalf("the job result reached the next request %d times:\n%s", got, next.Prompt)
	}
	if roles := len(c.History()); roles != 7 {
		t.Fatalf("history = %+v", c.History())
	}
}
