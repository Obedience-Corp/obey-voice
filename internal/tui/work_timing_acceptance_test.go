package tui

import (
	"bytes"
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/work"
	tea "github.com/charmbracelet/bubbletea"
)

// timingTerminal detects the first actual output write containing a target.
// Measuring model.Update or calling View directly would omit Bubble Tea's
// update/renderer boundary and would not be a UI timing observation.
type timingTerminal struct {
	mu     sync.Mutex
	out    bytes.Buffer
	target string
	seen   chan time.Time
}

func (w *timingTerminal) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	_, _ = w.out.Write(p)
	if w.target != "" && strings.Contains(stripANSI(w.out.String()), w.target) && w.seen != nil {
		select {
		case w.seen <- time.Now():
			w.seen = nil
		default:
		}
	}
	return len(p), nil
}

func (w *timingTerminal) arm(target string) <-chan time.Time {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.target = target
	w.seen = make(chan time.Time, 1)
	return w.seen
}

func nearestRankTiming(values []time.Duration, p float64) time.Duration {
	sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
	index := int(math.Ceil(float64(len(values))*p)) - 1
	if index < 0 {
		index = 0
	}
	return values[index]
}

func TestWorkTimingAcceptanceUsesRenderedTerminalFrames(t *testing.T) {
	scope := work.WorkScope{SessionID: "timing", RuntimeEpoch: "runtime", ConversationEpoch: "conversation"}
	var handoff []time.Duration
	for i := 0; i < 30; i++ {
		target := fmt.Sprintf("timing-handoff-%02d", i)
		handoff = append(handoff, measureTimingSample(t, scope, target, func(p *tea.Program, c *work.Coordinator) error {
			p.Send(busEventMsg{event: events.RouteDecided{Scope: scope, State: "accepted", Tier: "simple", JobID: "job-" + target, Task: target}})
			_, err := c.Register(scope, work.Admission{JobID: "job-" + target, Task: target, Origin: "agency", Provider: "fixture", Model: "fixture"})
			return err
		}))
	}

	var visibility []time.Duration
	for i := 0; i < 30; i++ {
		target := fmt.Sprintf("timing-result-%02d", i)
		visibility = append(visibility, measureResultSample(t, scope, target))
	}

	handoffP50 := nearestRankTiming(append([]time.Duration(nil), handoff...), .5)
	handoffP95 := nearestRankTiming(append([]time.Duration(nil), handoff...), .95)
	visibilityP50 := nearestRankTiming(append([]time.Duration(nil), visibility...), .5)
	visibilityP95 := nearestRankTiming(append([]time.Duration(nil), visibility...), .95)
	t.Logf("rendered work timing: handoff p50=%s p95=%s; result visibility p50=%s p95=%s", handoffP50, handoffP95, visibilityP50, visibilityP95)
	if handoffP95 > 100*time.Millisecond {
		t.Fatalf("visible handoff p95=%s exceeds 100ms", handoffP95)
	}
	if visibilityP95 > 250*time.Millisecond {
		t.Fatalf("result visibility p95=%s exceeds 250ms", visibilityP95)
	}
}

func measureTimingSample(t *testing.T, scope work.WorkScope, target string, publish func(*tea.Program, *work.Coordinator) error) time.Duration {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	m := sizedConversation(t, 100, 28)
	m.deps.runner = &fakeTurnRunner{}
	h := &recoveryTerminal{model: m}
	w := &timingTerminal{}
	p := tea.NewProgram(h, tea.WithInput(nil), tea.WithOutput(w), tea.WithContext(ctx), tea.WithoutSignalHandler())
	done := make(chan error, 1)
	go func() { _, err := p.Run(); done <- err }()
	defer func() { p.Quit(); <-done }()
	c := work.NewCoordinator(scope, work.WithChangeCallback(func(snapshot work.Snapshot) {
		p.Send(busEventMsg{event: events.WorkStateChanged{Snapshot: snapshot}})
	}))
	started := time.Now()
	observed := w.arm(target)
	if err := publish(p, c); err != nil {
		t.Fatalf("publish %q: %v", target, err)
	}
	select {
	case at := <-observed:
		return at.Sub(started)
	case <-ctx.Done():
		t.Fatalf("%q was not rendered: %v", target, ctx.Err())
		return 0
	case <-time.After(time.Second):
		t.Fatalf("%q was not rendered", target)
		return 0
	}
}

func measureResultSample(t *testing.T, scope work.WorkScope, target string) time.Duration {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	m := sizedConversation(t, 100, 28)
	m.deps.runner = &fakeTurnRunner{}
	h := &recoveryTerminal{model: m}
	w := &timingTerminal{}
	p := tea.NewProgram(h, tea.WithInput(nil), tea.WithOutput(w), tea.WithContext(ctx), tea.WithoutSignalHandler())
	done := make(chan error, 1)
	go func() { _, err := p.Run(); done <- err }()
	defer func() { p.Quit(); <-done }()
	c := work.NewCoordinator(scope, work.WithChangeCallback(func(snapshot work.Snapshot) {
		p.Send(busEventMsg{event: events.WorkStateChanged{Snapshot: snapshot}})
	}))
	key, err := c.Register(scope, work.Admission{JobID: "result-job-" + target, Task: "result task", Origin: "agency", Provider: "fixture", Model: "fixture"})
	if err != nil {
		t.Fatalf("register %q: %v", target, err)
	}
	// Register may render the queued task, but the ready marker is absent until
	// Publish. Arm immediately before the canonical publication boundary.
	started := time.Now()
	observed := w.arm(target)
	if _, err := c.Publish(key, work.Outcome{Scope: scope, JobID: key.JobID, ResultID: target, Status: work.OutcomeSucceeded, Text: "ready " + target}); err != nil {
		t.Fatalf("publish result %q: %v", target, err)
	}
	select {
	case at := <-observed:
		return at.Sub(started)
	case <-ctx.Done():
		t.Fatalf("result %q was not rendered: %v", target, ctx.Err())
		return 0
	case <-time.After(time.Second):
		t.Fatalf("result %q was not rendered", target)
		return 0
	}
}
