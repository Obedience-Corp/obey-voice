package personaruntime

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/internal/persona"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/job"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/pipeline"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/session"
)

const (
	claudeSummary = "Summary: Mira noted the cobalt vault code."
	claudeStall   = "STALL"
	claudePartial = "The vault code is"
)

// installClaudeFixture puts a fake claude executable on PATH. It re-runs this
// test binary, logs each request, and answers in the CLI's stream-json or
// json shape with a reply that carries the speaker label Claude sometimes adds.
func installClaudeFixture(t *testing.T) string {
	t.Helper()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	script := "#!/bin/sh\nexec '" + binary + "' -test.run='^TestClaudeSelectorFixtureProcess$' -- \"$@\"\n"
	if err := os.WriteFile(filepath.Join(dir, "claude"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(dir, "claude.jsonl")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("OBEY_CLAUDE_FIXTURE_LOG", log)
	return log
}

type claudeRequest struct{ Prompt, Format, Resume string }

func TestClaudeSelectorFixtureProcess(t *testing.T) {
	log := os.Getenv("OBEY_CLAUDE_FIXTURE_LOG")
	if log == "" {
		return
	}
	args := os.Args
	for i, a := range args {
		if a == "--" {
			args = args[i+1:]
			break
		}
	}
	var req claudeRequest
	for i := 0; i+1 < len(args); i++ {
		switch args[i] {
		case "-p":
			req.Prompt = args[i+1]
		case "--output-format":
			req.Format = args[i+1]
		case "--resume":
			req.Resume = args[i+1]
		}
	}
	f, err := os.OpenFile(log, os.O_CREATE|os.O_APPEND|os.O_RDWR, 0o600)
	if err != nil {
		os.Exit(2)
	}
	line, _ := json.Marshal(req)
	_, _ = f.Write(append(line, '\n'))
	_ = f.Close()
	data, _ := os.ReadFile(log)
	n := bytes.Count(data, []byte("\n"))
	out := json.NewEncoder(os.Stdout)
	if req.Format == "json" {
		_ = out.Encode(map[string]any{"type": "result", "subtype": "success", "result": claudeSummary, "session_id": "claude-fixture"})
		os.Exit(0)
	}
	text := fmt.Sprintf("Mira: Mira reply %d.", n)
	if strings.Contains(req.Prompt, claudeStall) {
		text = "Mira: " + claudePartial
	}
	_ = out.Encode(map[string]any{"type": "system", "subtype": "init", "session_id": "claude-fixture"})
	_ = out.Encode(map[string]any{"type": "assistant", "session_id": "claude-fixture", "message": map[string]any{"content": []map[string]string{{"type": "text", "text": text}}}})
	if strings.Contains(req.Prompt, claudeStall) {
		time.Sleep(time.Minute)
		os.Exit(0)
	}
	_ = out.Encode(map[string]any{"type": "result", "subtype": "success", "result": text, "session_id": "claude-fixture"})
	os.Exit(0)
}

func readClaudeRequests(t *testing.T, log string) []claudeRequest {
	t.Helper()
	f, err := os.Open(log)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	var out []claudeRequest
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		var req claudeRequest
		if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
			t.Fatal(err)
		}
		out = append(out, req)
	}
	return out
}

type ollamaMessage struct{ Role, Content string }

// claudeConversation is a conversation that starts on Mira, a Claude persona,
// with Ember on a fake Ollama server as the other participant.
type claudeConversation struct {
	t        *testing.T
	log      string
	base     *config.Config
	catalog  persona.Catalog
	store    *session.Store
	sess     *session.Session
	p        *pipeline.Pipeline
	selector *Selector
	initial  brain.Provider
	work     bool
	mu       sync.Mutex
	ollama   [][]ollamaMessage
	ready    []events.ResponseReady
}

func newClaudeConversation(t *testing.T, maxHistory int) *claudeConversation {
	t.Helper()
	c := &claudeConversation{t: t, log: installClaudeFixture(t)}
	root, _ := filepath.EvalSymlinks(t.TempDir())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/api/chat" {
			_, _ = w.Write([]byte(`{"models":[{"name":"fixture:latest"}]}`))
			return
		}
		var req struct{ Messages []ollamaMessage }
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		c.mu.Lock()
		c.ollama = append(c.ollama, req.Messages)
		c.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"model": "fixture:latest", "message": map[string]string{"role": "assistant", "content": emberReply}, "done": true, "done_reason": "stop"})
	}))
	t.Cleanup(server.Close)
	c.catalog = persona.Catalog{Dir: filepath.Join(root, "personas")}
	promptDir := filepath.Join(root, "prompts")
	for id, display := range map[string]string{"mira": "Mira", "ember": "Ember"} {
		profile := testProfile(id, display, id)
		profile.Brain.Model = ""
		if id == "mira" {
			profile.Brain.Provider = "claude"
		}
		writeTestProfile(t, c.catalog.Dir, profile)
		writeTestPrompt(t, promptDir, id, "You are {agent_name}.")
	}
	c.base = &config.Config{ActivePersona: "mira", BrainProvider: "claude", OllamaModel: "fixture:latest", OllamaHost: server.URL,
		PromptsDir: promptDir, WorkDir: root, MaxHistory: maxHistory, OllamaNumCtx: 8192}
	c.store = session.NewStore(filepath.Join(root, "sessions"))
	c.sess = session.New("claude", "")
	c.start("mira", nil, nil)
	return c
}

// start binds a fresh pipeline and selector to the conversation, as a host
// does at launch or resume.
func (c *claudeConversation) start(id string, turns []brain.Turn, contexts brain.PersonaContexts) {
	t := c.t
	t.Helper()
	binding, err := c.catalog.ResolveBinding(c.base, id)
	if err != nil {
		t.Fatal(err)
	}
	initial, err := brain.NewProvider(binding.Config())
	if err != nil {
		t.Fatal(err)
	}
	if len(turns) > 0 {
		initial.LoadHistory(turns)
	}
	p := &pipeline.Pipeline{Brain: initial, Events: events.NewBus()}
	if c.work {
		jobs := job.NewTable(t.Context())
		t.Cleanup(jobs.Close)
		pipeline.NewAgency(p, jobs, nil, binding.Config(), nil).Wire()
		if err := p.EnableConversationWork(c.sess.ID, nil, binding.Config()); err != nil {
			t.Fatal(err)
		}
	} else if err := p.BeginConversationIdentity(c.sess.ID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := p.CloseBrains(); err != nil {
			t.Error(err)
		}
	})
	selector, err := NewSelector(SelectionOptions{Base: c.base, Catalog: c.catalog, Pipeline: p, SessionID: c.sess.ID, Initial: binding,
		Transcript: turns, InitialContexts: contexts,
		PersistContexts: func(turns []brain.Turn, s events.PersonaSelection, contexts brain.PersonaContexts) error {
			return c.store.SaveSelectionState(c.sess, turns, s, contexts)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	p.AdmitInput = selector.Route
	p.OnTurn = func() {
		if err := c.store.Save(c.sess, selector.Transcript(), selector.ContextSnapshots()); err != nil {
			t.Error(err)
		}
	}
	events.Subscribe(p.Events, func(e events.ResponseReady) {
		c.mu.Lock()
		c.ready = append(c.ready, e)
		c.mu.Unlock()
	})
	c.p, c.selector, c.initial = p, selector, initial
}

func (c *claudeConversation) say(inputs ...string) {
	c.t.Helper()
	ctx, cancel := context.WithTimeout(c.t.Context(), 30*time.Second)
	defer cancel()
	for _, input := range inputs {
		if err := c.p.RunTurnTextMode(ctx, input); err != nil {
			c.t.Fatalf("%q: %v", input, err)
		}
	}
}

func (c *claudeConversation) responses() []events.ResponseReady {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]events.ResponseReady(nil), c.ready...)
}

func (c *claudeConversation) ollamaRequests() [][]ollamaMessage {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([][]ollamaMessage(nil), c.ollama...)
}

func (c *claudeConversation) saved() *session.Session {
	c.t.Helper()
	loaded, err := c.store.Load(c.sess.ID)
	if err != nil {
		c.t.Fatal(err)
	}
	return loaded
}

func replies(turns []brain.Turn) []brain.Turn {
	var out []brain.Turn
	for _, turn := range turns {
		if turn.Role == "assistant" {
			out = append(out, turn)
		}
	}
	return out
}

func TestConversationStartedOnClaudeOwnsAttributedTranscript(t *testing.T) {
	c := newClaudeConversation(t, 2)
	inputs := []string{"Note that the vault code is cobalt.", "The door is teal.", "Remember both."}
	c.say(inputs...)

	transcript := c.selector.Transcript()
	ready := c.responses()
	if len(transcript) != 6 || len(ready) != 3 {
		t.Fatalf("transcript = %+v, responses = %d", transcript, len(ready))
	}
	prompt := c.selector.Snapshot().PromptHash
	for i, input := range inputs {
		user, reply := transcript[2*i], transcript[2*i+1]
		if user.Role != "user" || user.Content != input {
			t.Fatalf("user turn %d = %+v", i, user)
		}
		want := fmt.Sprintf("Mira reply %d.", i+1)
		if reply.Content != want || reply.Content != ready[i].Response || ready[i].Envelope == nil || reply.ResponseID != ready[i].Envelope.ResponseID ||
			reply.PersonaID != "mira" || reply.PersonaDisplayName != "Mira" || reply.PromptHash != prompt || prompt == "" {
			t.Fatalf("reply %d = %+v, response = %+v", i, reply, ready[i])
		}
	}
	own := c.initial.History()
	if len(own) >= len(transcript) {
		t.Fatalf("provider history was not trimmed: %+v", own)
	}
	for _, turn := range own {
		if turn.PersonaID != "" || turn.ResponseID != "" {
			t.Fatalf("provider stored identity itself: %+v", turn)
		}
	}

	c.say("switch to Ember", "What color is the door?")
	final := c.selector.Transcript()
	if len(final) != 10 {
		t.Fatalf("transcript after switch = %+v", final)
	}
	for i, turn := range transcript {
		if final[i].Role != turn.Role || final[i].Content != turn.Content || final[i].PersonaID != turn.PersonaID || final[i].ResponseID != turn.ResponseID {
			t.Fatalf("turn %d changed after the switch: %+v, was %+v", i, final[i], turn)
		}
	}
	if got := replies(final); got[3].PersonaID != "ember" || got[3].Content != "Ember here." || got[4].PersonaID != "ember" || got[4].Content != emberReply {
		t.Fatalf("Ember replies = %+v", got[3:])
	}
	requests := c.ollamaRequests()
	if len(requests) != 1 {
		t.Fatalf("ollama requests = %d", len(requests))
	}
	var sent strings.Builder
	for _, m := range requests[0] {
		sent.WriteString(m.Role + ": " + m.Content + "\n")
		if m.Role == "assistant" && strings.Contains(m.Content, "Mira reply") {
			t.Fatalf("Mira's reply was replayed as Ember's own:\n%s", sent.String())
		}
	}
	if !strings.Contains(requests[0][0].Content, "You are Ember.") || !strings.Contains(sent.String(), "Mira reply 3.") || !strings.Contains(sent.String(), "Remember both.") {
		t.Fatalf("Ember did not receive the attributed conversation:\n%s", sent.String())
	}
	if strings.Contains(sent.String(), "Mira: ") {
		t.Fatalf("Claude's speaker label reached the transcript:\n%s", sent.String())
	}

	saved := c.saved()
	if saved.SelectedPersonaID != "ember" || len(saved.Turns) != len(final) {
		t.Fatalf("saved session = %+v", saved)
	}
	for i := range final {
		if saved.Turns[i].Content != final[i].Content || saved.Turns[i].PersonaID != final[i].PersonaID || saved.Turns[i].ResponseID != final[i].ResponseID {
			t.Fatalf("saved turn %d = %+v, want %+v", i, saved.Turns[i], final[i])
		}
	}
	for _, req := range readClaudeRequests(t, c.log) {
		if strings.Contains(req.Prompt, "You are Ember.") {
			t.Fatalf("Claude received Ember's identity: %q", req.Prompt)
		}
	}
}

func TestClaudeStartedConversationStillRefusesSwitchToAnotherClaudePersona(t *testing.T) {
	c := newClaudeConversation(t, 20)
	nova := testProfile("nova", "Nova", "nova")
	nova.Brain.Provider, nova.Brain.Model = "claude", ""
	writeTestProfile(t, c.catalog.Dir, nova)
	writeTestPrompt(t, c.base.PromptsDir, "nova", "You are {agent_name}.")
	_, err := c.selector.Select(t.Context(), "nova", "explicit")
	if err == nil || !strings.Contains(err.Error(), `provider "claude" has no verified shared history projection`) {
		t.Fatalf("err = %v", err)
	}
	if got := c.selector.Snapshot().PersonaID; got != "mira" {
		t.Fatalf("selection moved to %q", got)
	}
}

func TestClaudeConversationRecordsInterruptedReplyAndKeepsCursors(t *testing.T) {
	c := newClaudeConversation(t, 20)
	c.say("Note that the vault code is cobalt.")
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	turnCtx, interrupt := context.WithCancel(ctx)
	var once sync.Once
	events.Subscribe(c.p.Events, func(events.ResponseDelta) { once.Do(interrupt) })
	if err := c.p.RunTurnTextMode(turnCtx, "What was it? "+claudeStall); err == nil {
		t.Fatal("interrupted turn reported success")
	}
	transcript := c.selector.Transcript()
	if len(transcript) != 4 {
		t.Fatalf("transcript = %+v", transcript)
	}
	partial := transcript[3]
	if partial.Content != claudePartial || partial.Disposition != "interrupted" || partial.PersonaID != "mira" || partial.ResponseID == "" || partial.ResponseID == transcript[1].ResponseID {
		t.Fatalf("interrupted reply = %+v", partial)
	}

	c.say("Say it again.", "switch to Ember", "What is the vault code?")
	final := c.selector.Transcript()
	if len(final) != 10 || final[3].Disposition != "interrupted" || final[5].Content != "Mira reply 3." || final[5].Disposition != "" {
		t.Fatalf("transcript after interruption = %+v", final)
	}
	contexts := c.selector.ContextSnapshots()
	ember, ok := contexts["ember"]
	if !ok || ember.Through != len(final) {
		t.Fatalf("Ember's cursor = %+v, transcript has %d turns", ember, len(final))
	}
	probe := ember
	probe.Working = nil
	if view := probe.Reconcile(final); len(view.Working) != 0 {
		t.Fatalf("Ember's cursor no longer matches the transcript; reconciled from scratch: %+v", view.Working)
	}
	var sent strings.Builder
	for _, m := range c.ollamaRequests()[0] {
		sent.WriteString(m.Role + ": " + m.Content + "\n")
	}
	if !strings.Contains(sent.String(), "Incomplete assistant response") {
		t.Fatalf("Ember did not see the interrupted reply as unfinished:\n%s", sent.String())
	}
}

func TestClaudeConversationResumesTranscriptSelectionAndCursors(t *testing.T) {
	c := newClaudeConversation(t, 20)
	c.say("Note that the vault code is cobalt.", "switch to Ember", "What is the vault code?")
	saved := c.saved()
	cursor := saved.PersonaContexts["ember"]
	if saved.SelectedPersonaID != "ember" || len(saved.Turns) != 6 || cursor.Through != 6 {
		t.Fatalf("saved session = %+v", saved)
	}

	c.start(saved.SelectedPersonaID, saved.Turns, saved.PersonaContexts)
	if got := c.selector.Snapshot(); got.PersonaID != "ember" {
		t.Fatalf("resumed selection = %+v", got)
	}
	resumed := c.selector.Transcript()
	if len(resumed) != len(saved.Turns) {
		t.Fatalf("resumed transcript = %+v", resumed)
	}
	for i := range resumed {
		if resumed[i].Content != saved.Turns[i].Content || resumed[i].PersonaID != saved.Turns[i].PersonaID || resumed[i].ResponseID != saved.Turns[i].ResponseID {
			t.Fatalf("resumed turn %d = %+v, saved %+v", i, resumed[i], saved.Turns[i])
		}
	}
	if got := c.selector.ContextSnapshots()["ember"]; got.Through != cursor.Through || got.PrefixHash != cursor.PrefixHash {
		t.Fatalf("resumed cursor = %+v, saved %+v", got, cursor)
	}
}

func TestClaudeConversationResumesWithoutLosingForeignTurns(t *testing.T) {
	c := newClaudeConversation(t, 2)
	earlier := []brain.Turn{
		{Role: "user", Content: "Check the vault."},
		{Role: "assistant", ToolCalls: []brain.TurnToolCall{{ID: "call-1", Name: "read", Arguments: `{"path":"vault"}`}}, PersonaID: "ember", PersonaDisplayName: "Ember", ResponseID: "earlier"},
		{Role: "tool", ToolName: "read", ToolCallID: "call-1", Content: "cobalt"},
		{Role: "assistant", Content: "The vault code is cobalt.", PersonaID: "ember", PersonaDisplayName: "Ember", ResponseID: "earlier"},
	}
	if err := c.store.SaveSelectionState(c.sess, earlier, events.PersonaSelection{PersonaID: "mira", DisplayName: "Mira"}); err != nil {
		t.Fatal(err)
	}
	saved := c.saved()
	c.start("mira", saved.Turns, saved.PersonaContexts)
	c.say("What did Ember find?")

	turns := c.saved().Turns
	if len(turns) != len(earlier)+2 {
		t.Fatalf("saved turns = %+v", turns)
	}
	for i, want := range earlier {
		got := turns[i]
		if got.Role != want.Role || got.Content != want.Content || got.PersonaID != want.PersonaID || got.ToolCallID != want.ToolCallID || len(got.ToolCalls) != len(want.ToolCalls) {
			t.Fatalf("turn %d = %+v, want %+v", i, got, want)
		}
	}
	if reply := turns[len(turns)-1]; reply.PersonaID != "mira" || reply.ResponseID == "" {
		t.Fatalf("new reply = %+v", reply)
	}
}

func TestClaudeConversationClearAndReloadRebindTheSamePersona(t *testing.T) {
	c := newClaudeConversation(t, 20)
	c.say("Note that the vault code is cobalt.")
	if err := c.p.ClearConversation(); err != nil {
		t.Fatal(err)
	}
	c.p.OnTurn()
	if turns := c.saved().Turns; len(turns) != 0 {
		t.Fatalf("cleared conversation was saved: %+v", turns)
	}
	c.say("Start over.")
	if turns := c.selector.Transcript(); len(turns) != 2 || turns[0].Content != "Start over." || turns[1].PersonaID != "mira" {
		t.Fatalf("transcript after clear = %+v", turns)
	}

	reloaded := c.base.Clone()
	reloaded.MaxHistory = 30
	if err := c.selector.Reload(t.Context(), reloaded); err != nil {
		t.Fatal(err)
	}
	c.say("Still there?", "And now?")
	turns := c.selector.Transcript()
	if len(turns) != 6 || turns[3].PersonaID != "mira" || turns[5].PersonaID != "mira" || c.selector.Snapshot().PersonaID != "mira" {
		t.Fatalf("transcript after reload = %+v", turns)
	}
	requests := readClaudeRequests(t, c.log)
	if len(requests) != 4 || requests[1].Resume != "" || requests[2].Resume != "" || requests[3].Resume != "claude-fixture" {
		t.Fatalf("clear and reload must start a fresh provider session, later turns resume it: %+v", requests)
	}
}

func TestClaudeCompactionKeepsTranscriptAttribution(t *testing.T) {
	c := newClaudeConversation(t, 20)
	c.say("Note that the vault code is cobalt.", "The door is teal.")
	before := c.selector.Transcript()
	c.p.CompactPrompt = "Summarize the conversation."
	if err := c.p.CompactConversation(t.Context()); err != nil {
		t.Fatal(err)
	}
	if own := c.initial.History(); len(own) == 0 || own[0].Content != claudeSummary {
		t.Fatalf("provider was not reseeded: %+v", own)
	}
	after := c.selector.Transcript()
	if len(after) != len(before) {
		t.Fatalf("compaction changed the transcript: %+v", after)
	}
	for i := range before {
		if after[i].Content != before[i].Content || after[i].PersonaID != before[i].PersonaID || after[i].ResponseID != before[i].ResponseID {
			t.Fatalf("turn %d = %+v, was %+v", i, after[i], before[i])
		}
	}
	requests := readClaudeRequests(t, c.log)
	if summary := requests[len(requests)-1]; summary.Format != "json" || !strings.Contains(summary.Prompt, "Historical attribution requirements") {
		t.Fatalf("compaction request = %+v", summary)
	}
	c.say("switch to Ember", "What is the vault code?")
	if got := replies(c.selector.Transcript()); len(got) != 4 || got[0].PersonaID != "mira" || got[1].PersonaID != "mira" || got[3].PersonaID != "ember" {
		t.Fatalf("replies after compaction and switch = %+v", got)
	}
}

func TestClaudeConversationKeepsOrdinaryTurnsWithGroupParticipationEnabled(t *testing.T) {
	c := newClaudeConversation(t, 20)
	c.base.GroupParticipationEnabled = true
	c.start("mira", nil, nil)
	c.say("Note that the vault code is cobalt.", "switch to Ember")
	if got := replies(c.selector.Transcript()); len(got) != 2 || got[0].PersonaID != "mira" || got[1].PersonaID != "ember" {
		t.Fatalf("replies = %+v", got)
	}
}

func TestClaudeRestoreDoesNotLookLikeAnOverdueCompaction(t *testing.T) {
	c := newClaudeConversation(t, 2)
	var long []brain.Turn
	for i := range 6 {
		long = append(long, brain.Turn{Role: "user", Content: fmt.Sprintf("Fact %d.", i)}, brain.Turn{Role: "assistant", Content: fmt.Sprintf("Noted %d.", i), PersonaID: "mira", PersonaDisplayName: "Mira"})
	}
	c.start("mira", long, nil)
	c.p.CompactPrompt, c.p.AutoCompactAfter = "Summarize the conversation.", 2*c.base.MaxHistory
	c.say("What was fact 5?")
	reloaded := c.base.Clone()
	reloaded.MaxHistory = 3
	if err := c.selector.Reload(t.Context(), reloaded); err != nil {
		t.Fatal(err)
	}
	c.say("And fact 4?")
	for _, req := range readClaudeRequests(t, c.log) {
		if req.Format == "json" {
			t.Fatalf("restoring the transcript triggered a summarize request: %+v", req)
		}
	}
	if turns := c.selector.Transcript(); len(turns) != len(long)+4 {
		t.Fatalf("transcript = %d turns, want %d", len(turns), len(long)+4)
	}
}

func TestClaudeTranscriptSurvivesClosingConversationWork(t *testing.T) {
	c := newClaudeConversation(t, 20)
	c.work = true
	c.start("mira", nil, nil)
	c.say("Note that the vault code is cobalt.")
	if err := c.p.CloseWork(); err != nil {
		t.Fatal(err)
	}
	if turns := c.selector.Transcript(); len(turns) != 2 || turns[1].PersonaID != "mira" {
		t.Fatalf("transcript after closing work = %+v", turns)
	}
	c.p.OnTurn()
	if turns := c.saved().Turns; len(turns) != 2 {
		t.Fatalf("final save = %+v", turns)
	}
}
