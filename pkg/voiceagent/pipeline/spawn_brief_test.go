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

// historyBrain is a fakeBrain that remembers a conversation, so a spawn can be
// tested against the history the talking brain actually holds.
type historyBrain struct {
	fakeBrain
	turns []brain.Turn
}

func (h *historyBrain) History() []brain.Turn { return h.turns }

// promptWorker records the prompt the worker was handed. That string is the
// whole point of this change: it used to be one utterance.
type promptWorker struct {
	fakeBrain
	mu     sync.Mutex
	prompt string
}

func (w *promptWorker) ThinkFull(_ context.Context, input string, _ brain.StreamOptions) (string, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.prompt = input
	return "done", nil
}

func (w *promptWorker) seen() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.prompt
}

func torpedoHistory() []brain.Turn {
	return []brain.Turn{
		{Role: "user", Content: "I need a design for a lightweight homing torpedo."},
		{Role: "assistant", Content: "A shaped charge and an acoustic seeker. Shall I draft the brief?"},
		{Role: "tool", Content: "[tool:route]\nroute=chat"},
		{Role: "assistant", Content: "This is a multi-phase build, so I will run it as a festival."},
	}
}

func TestSpawnBriefCarriesTheConversationAndMarksTheTrigger(t *testing.T) {
	brief := spawnBrief(torpedoHistory(), "Yes, please begin.")

	for _, want := range []string{
		"lightweight homing torpedo",
		"run it as a festival",
		"The user then said: Yes, please begin.",
	} {
		if !strings.Contains(brief, want) {
			t.Errorf("brief missing %q:\n%s", want, brief)
		}
	}
	// The trigger is labeled as the trigger, not presented as the whole task.
	if strings.HasPrefix(strings.TrimSpace(brief), "Yes") {
		t.Errorf("brief opens with the bare utterance:\n%s", brief)
	}
}

// The instruction that answers the incident: a worker that cannot tell what it
// was asked must not go find something else to do.
func TestSpawnBriefForbidsGoingHunting(t *testing.T) {
	brief := spawnBrief(torpedoHistory(), "Yes.")
	for _, want := range []string{"Do not search for other work", "not your task"} {
		if !strings.Contains(brief, want) {
			t.Errorf("brief missing the no-hunting instruction %q:\n%s", want, brief)
		}
	}
}

// Tool traffic is the talking brain's own bookkeeping; a different provider's
// worker cannot use it and it only spends the budget.
func TestSpawnBriefDropsToolTurns(t *testing.T) {
	brief := spawnBrief(torpedoHistory(), "Do it")
	if strings.Contains(brief, "[tool:route]") {
		t.Errorf("brief carried tool traffic:\n%s", brief)
	}
}

func TestSpawnBriefIsBounded(t *testing.T) {
	var long []brain.Turn
	for i := 0; i < 200; i++ {
		long = append(long, brain.Turn{Role: "user", Content: strings.Repeat("x", 500)})
	}
	brief := spawnBrief(long, "go ahead")
	// The conversation block is clipped; the framing and trigger ride on top.
	if len(brief) > spawnBriefBytes+1024 {
		t.Fatalf("brief is %d bytes, want it bounded near %d", len(brief), spawnBriefBytes)
	}
}

func TestSpawnBriefUsesSpeakerLabels(t *testing.T) {
	brief := spawnBrief([]brain.Turn{
		{Role: "user", Speaker: "speaker-2", Content: "Pull the quarterly numbers."},
	}, "yes")
	if !strings.Contains(brief, "speaker-2: Pull the quarterly numbers.") {
		t.Errorf("brief lost the speaker label:\n%s", brief)
	}
}

func TestIsBareContinuation(t *testing.T) {
	bare := []string{
		"Yes", "yes.", "Yeah!", "sure", "OK", "okay", "please",
		"Do it.", "do that", "go ahead", "Continue, please.", "keep going",
		"yes please", "ok sure", "yes and continue", "  ", "Proceed.",
	}
	for _, s := range bare {
		if !isBareContinuation(s) {
			t.Errorf("%q should read as a bare continuation", s)
		}
	}
	real := []string{
		"search the web for torpedo seekers",
		"yes, search the web for that",
		"do it in the ironman campaign",
		"continue the festival scaffold",
		"build this",
	}
	for _, s := range real {
		if isBareContinuation(s) {
			t.Errorf("%q carries a task and must not read as bare", s)
		}
	}
}

func TestSpawnLacksTask(t *testing.T) {
	if !spawnLacksTask(nil, "Yes.") {
		t.Error("an assent with no conversation behind it has no task")
	}
	if spawnLacksTask(torpedoHistory(), "Yes.") {
		t.Error("an assent with a conversation behind it is a task")
	}
	if spawnLacksTask(nil, "search the web for torpedo seekers") {
		t.Error("a stated request is a task even with no history")
	}
	// Tool-only history is no history.
	if !spawnLacksTask([]brain.Turn{{Role: "tool", Content: "[tool:route]"}}, "do it") {
		t.Error("tool traffic is not a conversation")
	}
}

// newSpawnAgency wires a pipeline whose brain holds turns and whose worker
// records its prompt.
func newSpawnAgency(t *testing.T, turns []brain.Turn) (*Agency, *promptWorker, *progressiveFakeTTS, func()) {
	t.Helper()
	provider := &progressiveFakeTTS{}
	player := newFakePlayer(time.Millisecond)
	worker := &promptWorker{}
	jobs := job.NewTable(context.Background())
	p := &Pipeline{
		TTS:    provider,
		Player: player,
		Events: events.NewBus(),
		Brain:  &historyBrain{turns: turns},
	}
	agency := NewAgency(p, jobs, worker, &config.Config{}, nil)
	agency.Wire()
	return agency, worker, provider, func() { player.Close() }
}

func routeSpawn(agency *Agency, transcript string) {
	p := agency.pipeline
	metrics := newTurnMetrics()
	turn := p.newTurnConductor(metrics)
	agency.HandleRoute(context.Background(), turn, metrics, RouteDecision{
		Kind:       RouteSpawn,
		Transcript: transcript,
		Source:     "test",
	})
}

// The regression this whole change exists for: the worker must receive the
// conversation, not the single word that triggered the spawn.
func TestSpawnHandsTheWorkerTheConversation(t *testing.T) {
	agency, worker, _, cleanup := newSpawnAgency(t, torpedoHistory())
	defer cleanup()

	routeSpawn(agency, "Yes, please begin.")

	deadline := time.After(2 * time.Second)
	for worker.seen() == "" {
		select {
		case <-deadline:
			t.Fatal("worker never ran")
		case <-time.After(5 * time.Millisecond):
		}
	}
	prompt := worker.seen()
	if prompt == "Yes, please begin." {
		t.Fatal("worker still receives the bare utterance")
	}
	if !strings.Contains(prompt, "lightweight homing torpedo") {
		t.Fatalf("worker prompt lost the conversation:\n%s", prompt)
	}
}

// An assent with nothing behind it must not start a job at all.
func TestSpawnOnABareAssentAsksInsteadOfGuessing(t *testing.T) {
	agency, worker, provider, cleanup := newSpawnAgency(t, nil)
	defer cleanup()

	routeSpawn(agency, "Yes.")

	if got := len(agency.jobs.Status()); got != 0 {
		t.Fatalf("started %d jobs on a bare assent, want 0", got)
	}
	if worker.seen() != "" {
		t.Fatalf("worker ran on a bare assent with prompt %q", worker.seen())
	}
	spoken := strings.Join(provider.Calls(), " ")
	if !strings.Contains(strings.ToLower(spoken), "what should i do") {
		t.Fatalf("assistant did not ask what to do; it said %q", spoken)
	}
	if strings.Contains(strings.ToLower(spoken), "working on it") {
		t.Fatalf("assistant acked work it never started: %q", spoken)
	}
}

// The label the user sees stays the utterance; only the worker's prompt grew.
func TestSpawnJobLabelStaysTheUtterance(t *testing.T) {
	agency, _, _, cleanup := newSpawnAgency(t, torpedoHistory())
	defer cleanup()

	routeSpawn(agency, "build this")

	status := agency.jobs.Status()
	if len(status) == 0 {
		t.Fatal("no job started")
	}
	if status[0].Task != "build this" {
		t.Fatalf("job label = %q, want the utterance", status[0].Task)
	}
}
