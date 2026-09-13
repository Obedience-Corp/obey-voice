package brain

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestWorkerBudgetCountsSharedActionsConcurrently(t *testing.T) {
	b := NewWorkerBudget(BudgetLimits{MaxRequests: 3, MaxToolCalls: 2, MaxRetries: 1})
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() { defer wg.Done(); _ = b.AcquireRequest(context.Background()) }()
	}
	wg.Wait()
	s := b.Snapshot()
	if s.Requests != 3 {
		t.Fatalf("requests = %d, want 3", s.Requests)
	}
	if err := b.AcquireTool(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := b.AcquireTool(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := b.AcquireTool(context.Background()); err == nil {
		t.Fatal("third tool was admitted")
	}
	outputBudget := NewWorkerBudget(BudgetLimits{MaxOutputTokens: 9})
	outputBudget.RecordUsage(0, 4)
	if got := outputBudget.RemainingOutput(); got != 5 {
		t.Fatalf("remaining output = %d, want 5", got)
	}
}

func TestWorkerBudgetRejectsExpiredRuntimeAction(t *testing.T) {
	b := NewWorkerBudget(BudgetLimits{MaxRuntime: time.Millisecond})
	time.Sleep(2 * time.Millisecond)
	if err := b.AcquireRequest(context.Background()); err == nil {
		t.Fatal("expired runtime admitted request")
	}
	if err := b.AcquireTool(context.Background()); err == nil {
		t.Fatal("expired runtime admitted tool")
	}
}

func TestWorkerBudgetStrictToolFence(t *testing.T) {
	b := NewWorkerBudget(BudgetLimits{StrictTools: true, MaxToolCalls: 5})
	s := &toolSession{budget: b}
	for _, tool := range s.tools() {
		if tool.Function.Name == "run_command" || tool.Function.Name == "fetch_url" || tool.Function.Name == "web_search" {
			t.Fatalf("strict schema exposes %s", tool.Function.Name)
		}
	}
	if err := b.AllowTool("run_command"); err == nil {
		t.Fatal("strict policy admitted subprocess")
	}
	if err := b.AllowTool("read_file"); err != nil {
		t.Fatal(err)
	}
}
