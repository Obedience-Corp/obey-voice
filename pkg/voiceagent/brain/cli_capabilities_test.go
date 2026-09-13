package brain

import (
	"context"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/lancekrogers/claude-code-go/pkg/claude"
	"github.com/lancekrogers/grok-go-sdk/pkg/grok"
)

func TestCLIWorkerCapabilitiesBoundaries(t *testing.T) {
	claude := CLIWorkerCapabilities("claude")
	if claude.RequestBoundary.Supported || !claude.RuntimeLimit.Supported || !claude.ProcessExit.Supported {
		t.Fatalf("claude observable limits = %+v", claude)
	}
	if claude.OutputTokenCeiling.Supported || claude.AuthoritativeBilling.Supported || claude.DescendantExit.Supported {
		t.Fatalf("claude overclaimed = %+v", claude)
	}
	grok := CLIWorkerCapabilities("grok")
	if !grok.Usage.Supported || grok.ChargeBound.Supported || grok.TurnBoundary.Supported {
		t.Fatalf("grok overclaimed = %+v", grok)
	}
	managed := CLIWorkerCapabilities("delegation")
	if !managed.RetryLimit.Supported || !managed.RuntimeLimit.Supported || managed.DescendantExit.Supported {
		t.Fatalf("managed capabilities = %+v", managed)
	}
}

func TestClaudeFullReportsObservedCharge(t *testing.T) {
	fake := &fakeClaudeClient{fullResults: []*claude.ClaudeResult{{Result: "ok", CostUSD: 0.000007}}}
	b := newTestBrain(fake)
	var got WorkerUsage
	_, err := b.ThinkFull(context.Background(), "hello", StreamOptions{OnWorkerUsage: func(u WorkerUsage) { got = u }})
	if err != nil {
		t.Fatal(err)
	}
	if got.ChargeMicroUSD == nil || *got.ChargeMicroUSD != 7 {
		t.Fatalf("usage=%+v", got)
	}
	if got.InputTokens != nil || got.OutputTokens != nil {
		t.Fatalf("unknown tokens must remain nil: %+v", got)
	}
}

func TestGrokFullReportsObservedCharge(t *testing.T) {
	fake := &fakeGrokClient{fullResults: []*grok.GrokResult{{Text: "ok", CostUSD: 0.000009}}}
	b := newTestGrokBrain(fake)
	var got WorkerUsage
	_, err := b.ThinkFull(context.Background(), "hello", StreamOptions{OnWorkerUsage: func(u WorkerUsage) { got = u }})
	if err != nil {
		t.Fatal(err)
	}
	if got.ChargeMicroUSD == nil || *got.ChargeMicroUSD != 9 {
		t.Fatalf("usage=%+v", got)
	}
	if got.InputTokens != nil || got.OutputTokens != nil {
		t.Fatalf("unknown tokens must remain nil: %+v", got)
	}
}

func TestCancellationDoesNotProveDescendantExit(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pidFile := t.TempDir() + "/child.pid"
	cmd := exec.CommandContext(ctx, "sh", "-c", `sleep 30 & child=$!; echo $child > "$1"; wait`, "sh", pidFile)
	// Cleanup owns this fixture's process group even if readiness fails.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { cancel(); _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); _ = cmd.Wait() }()
	pid := 0
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		b, err := os.ReadFile(pidFile)
		if err == nil {
			pid, _ = strconv.Atoi(strings.TrimSpace(string(b)))
			if pid > 0 {
				break
			}
		}
		time.Sleep(time.Millisecond)
	}
	if pid <= 0 {
		t.Fatal("helper did not publish child readiness")
	}
	cancel()
	if err := cmd.Wait(); err == nil {
		t.Fatal("helper survived cancellation")
	}
	if err := syscall.Kill(pid, 0); err != nil {
		t.Fatalf("child %d not observable after helper cancellation: %v", pid, err)
	}
}
