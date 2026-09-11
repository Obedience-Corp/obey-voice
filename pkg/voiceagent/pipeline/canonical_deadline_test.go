package pipeline

import (
	"context"
	"strings"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
)

func TestCanonicalGenerationDeadlineFallsBackOnceWithoutNewInput(t *testing.T) {
	p, c, scope := newCompletionLeasePipeline(t, false)
	base := &handoffCompletionBrain{lifecycleWorker: &lifecycleWorker{reply: "foreground"}}
	provider := &replacementCompletionBrain{handoffCompletionBrain: base, entered: make(chan struct{})}
	p.Brain = provider
	ctx, cancel := context.WithTimeout(context.Background(), 2*brain.CompletionDeadline)
	defer cancel()
	if err := p.RunBackgroundTurn(ctx); err != nil {
		t.Fatal(err)
	}
	s := c.Snapshot(scope)
	if len(s.Pending) != 0 || len(s.Communicated) != 1 || len(provider.History()) != 1 || !strings.Contains(provider.History()[0].Content, "pending result") {
		t.Fatalf("deadline did not commit one canonical brief: state=%+v history=%+v", s, provider.History())
	}
	if err := p.RunBackgroundTurn(ctx); err != nil {
		t.Fatal(err)
	}
	if len(provider.History()) != 1 {
		t.Fatal("completed fallback was repeated")
	}
}
