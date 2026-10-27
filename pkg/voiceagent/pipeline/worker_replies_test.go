package pipeline

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/job"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/session"
)

func TestWorkerSpeechKeepsOriginatingVoiceAfterPersonaSwitch(t *testing.T) {
	for _, clear := range []bool{false, true} {
		obey, samantha := &fakeTTS{}, &fakeTTS{}
		player := newFakePlayer(time.Millisecond)
		defer player.Close()
		worker := &slowWorker{started: make(chan struct{}), done: make(chan struct{}), reply: "The worker has finished checking the files."}
		p := &Pipeline{TTS: obey, Player: player, Events: events.NewBus()}
		if err := p.BeginConversationIdentity("voice-owner"); err != nil {
			t.Fatal(err)
		}
		jobs := job.NewTable(t.Context())
		defer jobs.Close()
		a := NewAgency(p, jobs, worker, &config.Config{}, nil)
		a.Wire()
		p.incActiveTurn()
		finished := make(chan struct{}, 1)
		jobs.OnDone(func(id string, state job.State, result string, err error) {
			a.onJobDone(id, state, result, err)
			finished <- struct{}{}
		})
		var heardOwner string
		events.Subscribe(p.Events, func(e events.SpeechSegmentReady) {
			if e.Text == worker.reply && e.Envelope != nil {
				heardOwner = e.Envelope.PersonaID
			}
		})
		metrics := newTurnMetrics()
		metrics.response = events.ResponseEnvelope{PersonaID: "obey", DisplayName: "Obey"}
		a.handleSpawn(t.Context(), p.newTurnConductor(metrics), metrics, RouteDecision{Kind: RouteSpawn, Transcript: "Check the files"})
		<-worker.started
		p.ReplaceTTS(samantha, nil)
		close(worker.done)
		select {
		case <-finished:
		case <-time.After(3 * time.Second):
			t.Fatal("missing worker completion")
		}
		if clear {
			if err := p.ClearConversation(); err != nil {
				t.Fatal(err)
			}
		}
		p.decActiveTurn()
		spoke := strings.Contains(strings.Join(obey.Texts(), " "), worker.reply)
		if spoke == clear || len(samantha.Texts()) != 0 || (!clear && heardOwner != "obey") {
			t.Fatalf("clear=%t: Obey=%q Samantha=%q owner=%q", clear, obey.Texts(), samantha.Texts(), heardOwner)
		}
		for _, released := range p.TTSRetentions(obey, nil) {
			select {
			case <-released:
			case <-time.After(time.Second):
				t.Fatal("voice lease leaked")
			}
		}
	}
}

func TestLegacyWorkerReplyReachesConversationAndSavedHistory(t *testing.T) {
	root := t.TempDir()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/tags" {
			t.Error("completion should not generate another model reply")
		}
		_, _ = w.Write([]byte(`{"models":[{"name":"fixture"}]}`))
	}))
	defer server.Close()
	live, err := brain.NewOllama(&config.Config{OllamaHost: server.URL, OllamaModel: "fixture", AgentName: "Other persona", WorkDir: root, PromptsDir: filepath.Join(root, "prompts")})
	if err != nil {
		t.Fatal(err)
	}
	worker := &slowWorker{started: make(chan struct{}), done: make(chan struct{}), reply: "The fixture has three files."}
	p := &Pipeline{Brain: live, Events: events.NewBus()}
	p.SetOutputMuted(true)
	if err := p.BeginConversationIdentity("worker-session"); err != nil {
		t.Fatal(err)
	}
	jobs := job.NewTable(t.Context())
	defer jobs.Close()
	a := NewAgency(p, jobs, worker, &config.Config{VoiceWorkerProvider: "ollama", VoiceWorkerModel: "fixture"}, nil)
	a.Wire()
	store := session.NewStore(filepath.Join(root, "sessions"))
	saved := session.New("ollama", "fixture")
	p.OnTurn = func() {
		if err := store.Save(saved, p.Brain.History()); err != nil {
			t.Error(err)
		}
	}
	finished := make(chan events.JobFinished, 1)
	events.Subscribe(p.Events, func(e events.JobFinished) { finished <- e })
	metrics := newTurnMetrics()
	metrics.response = events.ResponseEnvelope{PersonaID: "jarvis", DisplayName: "Jarvis", ResponseID: "original-request", PromptHash: "frozen-prompt"}
	a.HandleRoute(t.Context(), p.newTurnConductor(metrics), metrics, RouteDecision{Kind: RouteSpawn, Transcript: "Inspect the fixture folder"})
	<-worker.started
	// Selection has changed before completion. The worker must retain its owner.
	metrics.response.DisplayName = "Other persona"
	close(worker.done)
	var e events.JobFinished
	select {
	case e = <-finished:
	case <-time.After(3 * time.Second):
		t.Fatal("missing visible completion")
	}
	if e.Summary != worker.reply || e.Envelope == nil || e.Envelope.DisplayName != "Jarvis" || e.Envelope.ResponseID == "original-request" {
		t.Fatalf("completion = %+v", e)
	}
	if len(live.History()) != 0 {
		t.Fatal("worker callback mutated foreground history")
	}
	if !p.BackgroundPending() {
		t.Fatal("muted output hid pending history delivery")
	}
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	if err := p.RunBackgroundTurn(cancelled); err != context.Canceled || !p.BackgroundPending() {
		t.Fatal("cancellation lost the pending result")
	}
	if err := p.RunBackgroundTurn(t.Context()); err != nil {
		t.Fatal(err)
	}
	if p.BackgroundPending() {
		t.Fatal("committed reply still pending")
	}
	if err := p.RunBackgroundTurn(t.Context()); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load(saved.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Turns) != 1 || loaded.Turns[0].Content != worker.reply || loaded.Turns[0].PersonaID != "jarvis" || loaded.Turns[0].ResponseID != e.Envelope.ResponseID {
		t.Fatalf("saved thread = %+v", loaded.Turns)
	}
}

func TestAttributedWorkerReplyDoesNotBlockCLIProviderAdmission(t *testing.T) {
	for name, provider := range map[string]brain.Provider{
		"claude": &brain.Brain{}, "cursor": &brain.CursorBrain{}, "grok": &brain.GrokBrain{},
	} {
		t.Run(name, func(t *testing.T) {
			p := &Pipeline{Brain: provider, Events: events.NewBus()}
			if err := p.BeginConversationIdentity("cli-worker-reply"); err != nil {
				t.Fatal(err)
			}
			jobs := job.NewTable(t.Context())
			defer jobs.Close()
			a := NewAgency(p, jobs, nil, &config.Config{}, nil)
			a.Wire()
			a.workerOrigins["job"] = workerOrigin{Scope: p.ConversationScope(), Envelope: events.ResponseEnvelope{PersonaID: "obey", DisplayName: "Obey", PromptHash: "frozen-prompt", ResponseID: "worker-response"}}
			store := session.NewStore(t.TempDir())
			saved := session.New(name, "fixture")
			p.OnTurn = func() {
				if err := store.Save(saved, provider.History()); err != nil {
					t.Error(err)
				}
			}
			a.onJobDone("job", job.StateDone, "The worker found three files.", nil)
			for _, input := range []string{"What did Obey find?", "Thanks"} {
				_, admitted, err := p.admitInput(t.Context(), input, false, newTurnMetrics())
				if err != nil || admitted.Request != input {
					t.Fatalf("input blocked: admission=%+v err=%v", admitted, err)
				}
			}
			if p.hasWorkerReplies() || len(provider.History()) != 1 {
				t.Fatal("reply remains queued or was duplicated")
			}
			loaded, err := store.Load(saved.ID)
			if err != nil {
				t.Fatal(err)
			}
			if len(loaded.Turns) != 1 {
				t.Fatalf("saved turns: %+v", loaded.Turns)
			}
			turn := loaded.Turns[0]
			if turn.Content != "The worker found three files." || turn.PersonaID != "obey" || turn.PersonaDisplayName != "Obey" || turn.PromptHash != "frozen-prompt" || turn.ResponseID != "worker-response" {
				t.Fatalf("lost worker identity: %+v", turn)
			}
		})
	}
}

func TestClearedConversationRejectsLateLegacyReply(t *testing.T) {
	p := &Pipeline{Events: events.NewBus()}
	if err := p.BeginConversationIdentity("same-session"); err != nil {
		t.Fatal(err)
	}
	jobs := job.NewTable(t.Context())
	defer jobs.Close()
	a := NewAgency(p, jobs, nil, &config.Config{}, nil)
	a.Wire()
	a.workerOrigins["old"] = workerOrigin{Scope: p.ConversationScope()}
	if err := p.ClearConversation(); err != nil {
		t.Fatal(err)
	}
	called := false
	events.Subscribe(p.Events, func(events.JobFinished) { called = true })
	a.onJobDone("old", job.StateDone, "Stale answer", nil)
	if called || p.hasWorkerReplies() {
		t.Fatal("old result entered a cleared conversation")
	}
}

type threadFixtureWorker struct {
	fakeBrain
	turns []brain.Turn
}

func (w *threadFixtureWorker) History() []brain.Turn { return w.turns }
func (w *threadFixtureWorker) ThinkFull(ctx context.Context, input string, opts brain.StreamOptions) (string, error) {
	w.turns = []brain.Turn{{Role: "user", Content: input}, {Role: "assistant", Content: "Checking the fixture.", ToolCalls: []brain.TurnToolCall{{ID: "read", Name: "read_file", Arguments: `{"path":"fixture.txt"}`}}}}
	call := brain.ToolCall{ID: "read", Name: "read_file", Summary: "fixture.txt"}
	opts.OnToolStart(call)
	w.turns = append(w.turns, brain.Turn{Role: "tool", Content: "three files", ToolName: "read_file"})
	opts.OnToolEnd(successfulToolResult(call, time.Millisecond))
	w.turns = append(w.turns, brain.Turn{Role: "assistant", Content: "Found three files."})
	return "Found three files.", nil
}

func TestWorkerThreadSnapshotsRetainMessagesAndToolResults(t *testing.T) {
	p := &Pipeline{Events: events.NewBus()}
	jobs := job.NewTable(t.Context())
	defer jobs.Close()
	a := NewAgency(p, jobs, nil, &config.Config{}, nil)
	a.Wire()
	done := make(chan struct{})
	jobs.OnDone(func(string, job.State, string, error) { close(done) })
	_, err := jobs.Start(t.Context(), job.Spec{Task: "Read the fixture", Run: a.workerRun("Read the fixture", &threadFixtureWorker{}, &config.Config{VoiceToolsEnabled: true})})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("worker did not finish")
	}
	items := p.Workers()
	if len(items) != 1 || len(items[0].Thread) != 4 || items[0].Thread[2].Content != "three files" {
		t.Fatalf("thread = %+v", items)
	}
	items[0].Thread[1].ToolCalls[0].Name = "mutated"
	if p.Workers()[0].Thread[1].ToolCalls[0].Name != "read_file" {
		t.Fatal("UI snapshot aliases worker thread")
	}
}

func TestCanonicalWorkerThreadSurvivesCompletion(t *testing.T) {
	cfg := &config.Config{VoiceWorkerProvider: "ollama", VoiceToolsEnabled: true}
	p := &Pipeline{Events: events.NewBus()}
	jobs := job.NewTable(t.Context())
	defer jobs.Close()
	a := NewAgency(p, jobs, &threadFixtureWorker{}, cfg, nil)
	a.Wire()
	if err := p.EnableConversationWork("thread-session", nil, cfg); err != nil {
		t.Fatal(err)
	}
	metrics := newTurnMetrics()
	metrics.response = events.ResponseEnvelope{PersonaID: "jarvis", DisplayName: "Jarvis"}
	a.handleSpawn(t.Context(), p.newTurnConductor(metrics), metrics, RouteDecision{Kind: RouteSpawn, Transcript: "Read the fixture"})
	deadline := time.Now().Add(3 * time.Second)
	for {
		items := p.Workers()
		if len(items) == 1 && !items[0].Active() {
			if len(items[0].Thread) != 4 || items[0].PersonaName != "Jarvis" {
				t.Fatalf("completed worker lost its thread or owner: %+v", items)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("worker did not complete: %+v", items)
		}
		time.Sleep(time.Millisecond)
	}
	if err := p.ClearConversation(); err != nil {
		t.Fatal(err)
	}
	if items := p.Workers(); len(items) != 0 {
		t.Fatalf("old worker leaked into cleared conversation: %+v", items)
	}
}
