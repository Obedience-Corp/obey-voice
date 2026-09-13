package tui

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
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/pipeline"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/work"
	tea "github.com/charmbracelet/bubbletea"
)

type acceptanceHistoryBrain struct {
	stubBrain
	mu    sync.Mutex
	turns []brain.Turn
}

func (b *acceptanceHistoryBrain) AppendAssistantResponse(text string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.turns = append(b.turns, brain.Turn{Role: "assistant", Content: text})
	return nil
}
func (b *acceptanceHistoryBrain) History() []brain.Turn {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]brain.Turn(nil), b.turns...)
}

type acceptanceBackgroundTerminal struct{ recoveryTerminal }

func (h *acceptanceBackgroundTerminal) Init() tea.Cmd { return h.model.backgroundRecoveryCmd() }
func (h *acceptanceBackgroundTerminal) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	_, cmd := h.recoveryTerminal.Update(msg)
	return h, cmd
}

func TestAcceptanceTerminalSchedulesCanonicalResultWithoutInputOrWakeEdge(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	jobs := job.NewTable(ctx)
	defer jobs.Close()
	b := &acceptanceHistoryBrain{}
	pipe := &pipeline.Pipeline{Brain: b, Events: events.NewBus()}
	pipe.Agency = pipeline.NewAgency(pipe, jobs, &stubBrain{}, &config.Config{}, nil)
	pipe.Agency.Wire()
	if err := pipe.EnableConversationWork("idle-acceptance", nil, &config.Config{}); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = pipe.CloseWork() }()
	c, scope := pipe.WorkCoordinator(), pipe.WorkSnapshot().Scope
	key, err := c.Register(scope, work.Admission{Task: "weather"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.Publish(key, work.Outcome{Scope: scope, JobID: key.JobID, Status: work.OutcomeSucceeded, Text: "Denver fixture forecast"}); err != nil {
		t.Fatal(err)
	}
	// Intentionally lose the ready edge before the terminal host begins. The
	// production recovery timer must observe the coordinator's pending level.
	select {
	case <-pipe.BackgroundReady():
	default:
		t.Fatal("missing initial ready edge")
	}
	communicated := make(chan events.WorkDelivery, 1)
	events.Subscribe(pipe.Events, func(e events.WorkDelivery) {
		if e.Stage == "announcement.communicated" {
			select {
			case communicated <- e:
			default:
			}
		}
	})
	m := sizedConversation(t, 100, 24)
	m.deps.ctx = ctx
	m.deps.runner = pipe
	h := &acceptanceBackgroundTerminal{recoveryTerminal: recoveryTerminal{model: m}}
	p := tea.NewProgram(h, tea.WithInput(nil), tea.WithOutput(h), tea.WithContext(ctx), tea.WithoutSignalHandler())
	done := make(chan error, 1)
	started := time.Now()
	go func() { _, err := p.Run(); done <- err }()
	select {
	case <-communicated:
	case <-ctx.Done():
		p.Quit()
		<-done
		t.Fatal("idle host did not communicate canonical result without input")
	}
	p.Quit()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("lost-edge recovery took %s", elapsed)
	}
	s := c.Snapshot(scope)
	if len(s.Pending) != 0 || len(s.Communicated) != 1 {
		t.Fatalf("pending=%d communicated=%d", len(s.Pending), len(s.Communicated))
	}
	receipt := s.Communicated[0].LastAttempt.Receipt
	if receipt.Mode != "text" || !receipt.ResponseCommitted {
		t.Fatalf("receipt=%+v", receipt)
	}
	history := b.History()
	if len(history) != 1 || history[0].Role != "assistant" || !strings.Contains(history[0].Content, "Denver fixture forecast") {
		t.Fatalf("no committed update or fabricated user input: %+v", history)
	}
	t.Logf("actual Bubble Tea recovery timer -> production pipeline completion -> committed canonical text receipt in %s; no user input or ready edge", time.Since(started))
}
