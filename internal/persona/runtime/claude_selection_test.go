package personaruntime

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/internal/persona"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent"
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
	claudeHandoff = "HANDOFF"
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

// claudeRequest is one fixture invocation: its prompt, flags, the session it
// answered in, its process id, and the length of its longest argument.
type claudeRequest struct {
	Prompt, Format, Resume, Session string
	PID, MaxArg                     int
}

var fixturePersonaName = regexp.MustCompile(`You are (\w+)\.`)

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
	name := "Mira"
	for _, a := range args {
		req.MaxArg = max(req.MaxArg, len(a))
	}
	// The SDK leaves -p without a value and pipes a large prompt on stdin.
	if len(args) > 0 && args[0] == "-p" && (len(args) == 1 || strings.HasPrefix(args[1], "--")) {
		data, _ := io.ReadAll(os.Stdin)
		req.Prompt = string(data)
	}
	for i := 0; i+1 < len(args); i++ {
		switch args[i] {
		case "-p":
			if !strings.HasPrefix(args[i+1], "--") {
				req.Prompt = args[i+1]
			}
		case "--output-format":
			req.Format = args[i+1]
		case "--resume":
			req.Resume = args[i+1]
		case "--system-prompt":
			if m := fixturePersonaName.FindStringSubmatch(args[i+1]); m != nil {
				name = m[1]
			}
		}
	}
	data, _ := os.ReadFile(log)
	n := bytes.Count(data, []byte("\n")) + 1
	// Distinct sessions let a test tell a resumed session from a new one.
	req.Session, req.PID = "claude-fixture", os.Getpid()
	if os.Getenv("OBEY_CLAUDE_FIXTURE_SESSIONS") == "distinct" {
		req.Session = req.Resume
		if req.Session == "" {
			req.Session = fmt.Sprintf("claude-session-%d", n)
		}
	}
	f, err := os.OpenFile(log, os.O_CREATE|os.O_APPEND|os.O_RDWR, 0o600)
	if err != nil {
		os.Exit(2)
	}
	line, _ := json.Marshal(req)
	_, _ = f.Write(append(line, '\n'))
	_ = f.Close()
	out := json.NewEncoder(os.Stdout)
	if req.Format == "json" {
		_ = out.Encode(map[string]any{"type": "result", "subtype": "success", "result": claudeSummary, "session_id": req.Session})
		os.Exit(0)
	}
	text := fmt.Sprintf("%s: %s reply %d.", name, name, n)
	// Only the current request triggers a scripted reply; a conversation
	// record line may quote an earlier request.
	var current strings.Builder
	for line := range strings.Lines(req.Prompt) {
		if !strings.HasPrefix(line, "{") {
			current.WriteString(line)
		}
	}
	stall := strings.Contains(current.String(), claudeStall)
	switch {
	case stall:
		text = name + ": " + claudePartial
	case strings.Contains(current.String(), claudeHandoff):
		text = name + ": Ask @ember about it."
	}
	_ = out.Encode(map[string]any{"type": "system", "subtype": "init", "session_id": req.Session})
	_ = out.Encode(map[string]any{"type": "assistant", "session_id": req.Session, "message": map[string]any{"content": []map[string]string{{"type": "text", "text": text}}}})
	if stall {
		time.Sleep(time.Minute)
		os.Exit(0)
	}
	_ = out.Encode(map[string]any{"type": "result", "subtype": "success", "result": text, "session_id": req.Session})
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

type ollamaMessage struct {
	Role, Content string
	ToolCalls     json.RawMessage `json:"tool_calls"`
}

// claudeConversation is a conversation between Mira, a persona on Claude or
// another hosted provider, and Ember, a persona on a fake Ollama server.
type claudeConversation struct {
	t        *testing.T
	provider string
	log      string
	base     *config.Config
	catalog  persona.Catalog
	store    *session.Store
	sess     *session.Session
	p        *pipeline.Pipeline
	selector *Selector
	initial  brain.Provider
	work     bool
	saveErr  error
	mu       sync.Mutex
	ollama   [][]ollamaMessage
	ready    []events.ResponseReady
	errors   []events.Error
	notices  []hostedNoticeEvent
	// emberSays replaces Ember's reply when set.
	emberSays string
	// vision makes the fake Ollama model accept images.
	vision bool
}

func newClaudeConversation(t *testing.T, maxHistory int) *claudeConversation {
	t.Helper()
	return newMixedConversation(t, maxHistory, "mira")
}

// newMixedConversation starts the conversation on the persona start.
func newMixedConversation(t *testing.T, maxHistory int, start string) *claudeConversation {
	t.Helper()
	return newConversation(t, maxHistory, start, "claude", "")
}

// newConversation starts a conversation between Mira, a persona on provider,
// and Ember on Ollama, on the persona start, with hosted_persona_sharing set
// to sharing.
func newConversation(t *testing.T, maxHistory int, start, provider, sharing string) *claudeConversation {
	t.Helper()
	c := &claudeConversation{t: t, provider: provider}
	switch provider {
	case "claude":
		c.log = installClaudeFixture(t)
	case "codex":
		c.log = installCodexSelectorFixture(t)
	default:
		c.log = installMixedProviderBinaries(t)
	}
	root, _ := filepath.EvalSymlinks(t.TempDir())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		c.mu.Lock()
		vision := c.vision
		c.mu.Unlock()
		if r.URL.Path == "/api/show" && vision {
			_, _ = w.Write([]byte(`{"capabilities":["completion","vision"]}`))
			return
		}
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
		reply := cmp.Or(c.emberSays, emberReply)
		c.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"model": "fixture:latest", "message": map[string]string{"role": "assistant", "content": reply}, "done": true, "done_reason": "stop"})
	}))
	t.Cleanup(server.Close)
	c.catalog = persona.Catalog{Dir: filepath.Join(root, "personas")}
	promptDir := filepath.Join(root, "prompts")
	for id, display := range map[string]string{"mira": "Mira", "ember": "Ember"} {
		profile := testProfile(id, display, id)
		profile.Brain.Model = ""
		if id == "mira" {
			profile.Brain.Provider = provider
		}
		writeTestProfile(t, c.catalog.Dir, profile)
		writeTestPrompt(t, promptDir, id, "You are {agent_name}.")
	}
	c.base = &config.Config{ActivePersona: "mira", BrainProvider: provider, OllamaModel: "fixture:latest", OllamaHost: server.URL,
		PromptsDir: promptDir, WorkDir: root, MaxHistory: maxHistory, OllamaNumCtx: 8192, HostedPersonaSharing: sharing}
	if provider == "obey" {
		c.base.ObeyProvider, c.base.ObeyModel, c.base.ObeyToolPolicy = "fixture", "fixture", "daemon"
	}
	c.store = session.NewStore(filepath.Join(root, "sessions"))
	c.sess = session.New(provider, "")
	c.start(start, nil, nil)
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
	events.Subscribe(p.Events, func(e events.Error) {
		c.mu.Lock()
		c.errors = append(c.errors, e)
		c.mu.Unlock()
	})
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
		Transcript: turns, InitialContexts: contexts, SessionProvider: c.sess.Provider, HostedNotified: c.sess.HostedNotified, HostedNotifiedWhole: c.sess.HostedNotifiedWhole,
		PersistHostedNotified: func(told, whole []string) error { return c.store.SaveHostedNotified(c.sess, told, whole) },
		PersistContexts: func(turns []brain.Turn, s events.PersonaSelection, contexts brain.PersonaContexts) error {
			if c.saveErr != nil {
				return c.saveErr
			}
			return c.store.SaveSelectionState(c.sess, turns, s, contexts)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	// Runs before CloseBrains, so the brains it parks are joined there.
	t.Cleanup(selector.Close)
	p.AdmitInput = selector.Route
	p.HostedNotice, p.LiveHostedNotice = selector.HostedNotice, selector.LiveHostedNotice
	events.Subscribe(p.Events, func(e events.Info) {
		sent := 0
		if data, err := os.ReadFile(c.log); err == nil {
			sent = bytes.Count(data, []byte("\n"))
		}
		c.mu.Lock()
		c.notices = append(c.notices, hostedNoticeEvent{message: e.Message, claudeRequests: sent})
		c.mu.Unlock()
	})
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

func (c *claudeConversation) errorEvents() []events.Error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]events.Error(nil), c.errors...)
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

func TestClaudeStartedConversationSwitchesBetweenClaudePersonas(t *testing.T) {
	t.Setenv("OBEY_CLAUDE_FIXTURE_SESSIONS", "distinct")
	c := newClaudeConversation(t, 20)
	nova := testProfile("nova", "Nova", "nova")
	nova.Brain.Provider, nova.Brain.Model = "claude", ""
	writeTestProfile(t, c.catalog.Dir, nova)
	writeTestPrompt(t, c.base.PromptsDir, "nova", "You are {agent_name}.")
	c.say("Note that the vault code is cobalt.", "switch to Nova", "What do you think?", "switch to Mira", "And you?")

	requests := readClaudeRequests(t, c.log)
	if len(requests) != 3 {
		t.Fatalf("requests = %+v", requests)
	}
	mira, novaFirst, miraAgain := requests[0], requests[1], requests[2]
	if novaFirst.Resume != "" || novaFirst.Session == mira.Session {
		t.Fatalf("Nova did not start its own session: %+v", novaFirst)
	}
	requireOmits(t, novaFirst.Prompt, "cobalt", "Mira reply 1.")
	if miraAgain.Resume != mira.Session {
		t.Fatalf("Mira's session did not survive Nova speaking: %+v", miraAgain)
	}
	requireContains(t, miraAgain.Prompt, `"content":"Nova here.","persona":"Nova"`, `"content":"Nova reply 2.","persona":"Nova"`, `"content":"Mira here.","persona":"Mira"`)
	requireOmits(t, miraAgain.Prompt, "cobalt", "Mira reply 1.")
	got := replies(c.selector.Transcript())
	if len(got) != 5 || got[0].PersonaID != "mira" || got[2].PersonaID != "nova" || got[2].Content != "Nova reply 2." || got[4].PersonaID != "mira" || got[4].Content != "Mira reply 3." {
		t.Fatalf("replies = %+v", got)
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

func TestClaudeConversationClearAndPromptEditRestartTheSessionButVoiceChangesKeepIt(t *testing.T) {
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
	reloaded.VADSilenceDuration = 1.3
	if err := c.selector.Reload(t.Context(), reloaded); err != nil {
		t.Fatal(err)
	}
	c.say("Still there?")
	writeTestPrompt(t, c.base.PromptsDir, "mira", "You are {agent_name}, and you speak briefly.")
	c.say("And now?", "Once more.")
	turns := c.selector.Transcript()
	if len(turns) != 8 || turns[7].PersonaID != "mira" || c.selector.Snapshot().PersonaID != "mira" {
		t.Fatalf("transcript = %+v", turns)
	}
	requests := readClaudeRequests(t, c.log)
	if len(requests) != 5 {
		t.Fatalf("requests = %+v", requests)
	}
	if requests[1].Resume != "" {
		t.Fatalf("a cleared conversation resumed its old session: %+v", requests[1])
	}
	if requests[2].Resume != "claude-fixture" {
		t.Fatalf("an audio settings change restarted the session: %+v", requests[2])
	}
	if requests[3].Resume != "" || requests[4].Resume != "claude-fixture" {
		t.Fatalf("a prompt edit must start a fresh session that later turns resume: %+v", requests[3:])
	}
}

func TestClaudeVoiceOnlyReloadKeepsProviderSession(t *testing.T) {
	for name, change := range map[string]func(*config.Config){
		"tts_voice":             func(c *config.Config) { c.TTSVoice = "am_michael" },
		"vad_silence_duration":  func(c *config.Config) { c.VADSilenceDuration = 1.3 },
		"barge_in_enabled":      func(c *config.Config) { c.BargeInEnabled = !c.BargeInEnabled },
		"tts_fallback_provider": func(c *config.Config) { c.TTSFallbackProvider = "say" },
		"tts_worker_url":        func(c *config.Config) { c.TTSWorkerURL = "http://127.0.0.1:9" },
	} {
		t.Run(name, func(t *testing.T) {
			c := newClaudeConversation(t, 20)
			live := c.p.Brain
			c.say("one", "two")
			reloaded := c.base.Clone()
			change(reloaded)
			if err := c.selector.Reload(t.Context(), reloaded); err != nil {
				t.Fatal(err)
			}
			c.say("three")
			requests := readClaudeRequests(t, c.log)
			if last := requests[len(requests)-1]; last.Resume != "claude-fixture" || c.p.Brain != live {
				t.Fatalf("%s change replaced the live provider session: %+v", name, last)
			}
			if turns := c.selector.Transcript(); len(turns) != 6 {
				t.Fatalf("transcript = %+v", turns)
			}
		})
	}
}

func TestClaudeVoiceOnlyReloadWarnsInsteadOfFailing(t *testing.T) {
	c := newClaudeConversation(t, 20)
	voices := &voiceagent.LiveTTSManager{RetireDelay: -1}
	t.Cleanup(voices.Close)
	assets := 0
	c.selector.opts.Voices = voices
	c.selector.opts.VoiceAssets = func(context.Context, *config.Config) error { assets++; return nil }
	c.selector.opts.NewVoice = func(context.Context, *config.Config) (*voiceagent.TTSSet, error) {
		return &voiceagent.TTSSet{Primary: selectorNativeVoice{}, VoiceNotice: errors.New("voice Harper is not installed; using the default"), FallbackWarning: errors.New("fallback voice is unavailable")}, nil
	}
	c.say("one")
	reloaded := c.base.Clone()
	reloaded.TTSWorkerURL = "http://127.0.0.1:9"
	if err := c.selector.Reload(t.Context(), reloaded); err != nil {
		t.Fatal(err)
	}
	if assets != 1 {
		t.Fatalf("voice assets checked %d times, want once before building the voice", assets)
	}
	stages := map[string]bool{}
	for _, e := range c.errorEvents() {
		stages[e.Stage] = true
	}
	if !stages["tts-voice"] || !stages[events.StageTTSFallback] {
		t.Fatalf("voice warnings were not reported: %+v", c.errorEvents())
	}
	c.say("two")
	if requests := readClaudeRequests(t, c.log); requests[len(requests)-1].Resume != "claude-fixture" {
		t.Fatalf("voice change restarted the session: %+v", requests)
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
	writeTestPrompt(t, c.base.PromptsDir, "mira", "You are {agent_name}, and you speak briefly.")
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

// With no persona the conversation could move to, routing must not touch an
// input: the model answers a bare name, and nothing switches persona.
func TestClaudeWithoutReachablePersonasLeavesEveryInputToTheModel(t *testing.T) {
	for name, setup := range map[string]func(*claudeConversation){
		"only the live persona": func(c *claudeConversation) {
			if err := os.RemoveAll(filepath.Join(c.catalog.Dir, "ember")); err != nil {
				t.Fatal(err)
			}
		},
		"shipped personas inheriting claude": func(c *claudeConversation) {
			if err := os.RemoveAll(filepath.Join(c.catalog.Dir, "ember")); err != nil {
				t.Fatal(err)
			}
			for _, p := range persona.Builtins(nil) {
				writeTestProfile(t, c.catalog.Dir, *p)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			c := newClaudeConversation(t, 20)
			setup(c)
			c.start("mira", nil, nil)
			c.say("Mira", "Ember, what do you think?", "switch to Ember", "Samantha, what do you think?")
			ready := c.responses()
			if len(ready) != 4 {
				t.Fatalf("responses = %+v", ready)
			}
			for i, r := range ready {
				if r.Response != fmt.Sprintf("Mira reply %d.", i+1) {
					t.Fatalf("response %d = %q, want the model's answer", i, r.Response)
				}
			}
			if got := c.selector.Snapshot().PersonaID; got != "mira" {
				t.Fatalf("selection moved to %q", got)
			}
			for _, req := range readClaudeRequests(t, c.log) {
				if strings.Contains(req.Prompt, "Other available agent personas") {
					t.Fatalf("Claude received a persona roster:\n%s", req.Prompt)
				}
			}
		})
	}
}

// oneWayProvider stands in for Grok and Cursor: a live provider that keeps
// neither the shared history nor a session the host can bring up to date.
type oneWayProvider struct{ brain.Provider }

func TestOneWayProviderRefusesImplicitSwitchItCouldNotReturnFrom(t *testing.T) {
	c := newClaudeConversation(t, 20)
	c.p.Brain = oneWayProvider{c.p.Brain}
	c.say("Note that the vault code is cobalt.")
	for _, input := range []string{"Ember, what do you think?", "Ember.", "Hey Ember"} {
		err := c.p.RunTurnTextMode(t.Context(), input)
		if err == nil || !strings.Contains(err.Error(), `say "switch to Ember"`) {
			t.Fatalf("%q: err = %v", input, err)
		}
		if got := c.selector.Snapshot().PersonaID; got != "mira" {
			t.Fatalf("%q moved the conversation to %q", input, got)
		}
	}
	if len(c.ollamaRequests()) != 0 {
		t.Fatal("a refused switch reached Ember")
	}
	nova := testProfile("nova", "Nova", "nova")
	nova.Brain.Provider, nova.Brain.Model = "claude", ""
	writeTestProfile(t, c.catalog.Dir, nova)
	if err := c.p.RunTurnTextMode(t.Context(), "Nova, what do you think?"); err == nil || !strings.Contains(err.Error(), `say "switch to Nova"`) {
		t.Fatalf("a persona on Claude can take the conversation and give it back: %v", err)
	}
	rex := testProfile("rex", "Rex", "rex")
	rex.Brain.Provider, rex.Brain.Model = "cursor", ""
	writeTestProfile(t, c.catalog.Dir, rex)
	if err := c.p.RunTurnTextMode(t.Context(), "Rex, what do you think?"); err == nil || strings.Contains(err.Error(), "switch to") || !strings.Contains(err.Error(), `provider "cursor" has no verified shared history projection`) {
		t.Fatalf("a persona that cannot take over was offered a switch: %v", err)
	}
	c.say("@ember what do you think?")
	if got := c.selector.Snapshot().PersonaID; got != "ember" {
		t.Fatalf("explicit mention did not hand over: %q", got)
	}
}

func TestClaudeAndOllamaPersonasAreAddressedByName(t *testing.T) {
	c := newClaudeConversation(t, 20)
	c.say("Note that the vault code is cobalt.", "Ember, what do you think?")
	if got := c.selector.Snapshot().PersonaID; got != "ember" || len(c.ollamaRequests()) != 1 {
		t.Fatalf("addressing Ember by name from Mira selected %q", got)
	}
	// Ollama's own addressee model, which the fake server does not answer.
	c.selector.opts.AddressResolver = addressResolverFunc(func(_ context.Context, r brain.AddressRequest) (brain.Addressee, error) {
		if strings.HasPrefix(r.Text, "Mira") {
			return brain.Addressee{Kind: "address", Name: "Mira"}, nil
		}
		return brain.Addressee{Kind: "none"}, nil
	})
	c.say("Mira, do you agree?")
	if got := c.selector.Snapshot().PersonaID; got != "mira" {
		t.Fatalf("addressing Mira by name from Ember selected %q", got)
	}
	c.selector.opts.AddressResolver = nil
	c.say("Ember.", "Hey Mira")
	got := replies(c.selector.Transcript())
	want := []string{"mira", "ember", "mira", "ember", "mira"}
	if len(got) != len(want) {
		t.Fatalf("replies = %+v", got)
	}
	for i, id := range want {
		if got[i].PersonaID != id {
			t.Fatalf("reply %d = %+v, want %s", i, got[i], id)
		}
	}
}

func TestClaudeOnlyConversationHandsOverOnlyOnExplicitSwitch(t *testing.T) {
	c := newClaudeConversation(t, 20)
	ember := testProfile("ember", "Ember", "ember")
	ember.Brain.Provider, ember.Brain.Model = "claude", ""
	writeTestProfile(t, c.catalog.Dir, ember)
	c.start("mira", nil, nil)
	c.p.RouteHandoff = c.selector.RouteHandoff
	c.say("Mira", "Ember, what do you think?", "Hey Ember", "Who else? "+claudeHandoff)
	if got := c.selector.Snapshot().PersonaID; got != "mira" {
		t.Fatalf("an implicit address or a reply moved the conversation to %q", got)
	}
	c.say("switch to Ember", "What do you think?", "@mira and you?")
	if got := c.selector.Snapshot().PersonaID; got != "mira" {
		t.Fatalf("explicit switch and mention ended on %q", got)
	}
	got := replies(c.selector.Transcript())
	want := []struct{ persona, content string }{
		{"mira", "Mira reply 1."}, {"mira", "Mira reply 2."}, {"mira", "Mira reply 3."}, {"mira", "Ask @ember about it."},
		{"ember", "Ember here."}, {"ember", "Ember reply 5."}, {"mira", "Mira reply 6."},
	}
	if len(got) != len(want) {
		t.Fatalf("replies = %+v", got)
	}
	for i, w := range want {
		if got[i].PersonaID != w.persona || got[i].Content != w.content {
			t.Fatalf("reply %d = %s/%q, want %s/%q", i, got[i].PersonaID, got[i].Content, w.persona, w.content)
		}
	}
	for _, req := range readClaudeRequests(t, c.log) {
		if strings.Contains(req.Prompt, "Other available agent personas") {
			t.Fatalf("Claude received a persona roster:\n%s", req.Prompt)
		}
	}
}

func TestClaudeReplyHandsOffByMentionWhereThePersonaCanReturn(t *testing.T) {
	c := newClaudeConversation(t, 20)
	c.p.RouteHandoff = c.selector.RouteHandoff
	c.mu.Lock()
	c.emberSays = "It looks fine to me."
	c.mu.Unlock()
	c.say("Who should look at this? " + claudeHandoff)
	if got := c.selector.Snapshot().PersonaID; got != "ember" || len(c.ollamaRequests()) != 1 {
		t.Fatalf("Mira's @ember did not hand the floor to Ember: selection %q", got)
	}
	got := replies(c.selector.Transcript())
	if len(got) != 2 || got[0].PersonaID != "mira" || got[1].PersonaID != "ember" || got[1].Content != "It looks fine to me." {
		t.Fatalf("replies = %+v", got)
	}

	oneWay := newClaudeConversation(t, 20)
	oneWay.p.Brain = oneWayProvider{oneWay.p.Brain}
	oneWay.p.RouteHandoff = oneWay.selector.RouteHandoff
	oneWay.say("Who should look at this? " + claudeHandoff)
	if got := oneWay.selector.Snapshot().PersonaID; got != "mira" || len(oneWay.ollamaRequests()) != 0 {
		t.Fatalf("a reply moved the conversation off a provider that cannot take it back: %q", got)
	}
}

func TestClaudeStartupSaveFailureWarnsInsteadOfAborting(t *testing.T) {
	c := newClaudeConversation(t, 20)
	c.saveErr = errors.New("disk full")
	c.start("mira", nil, nil)
	reported := false
	for _, e := range c.errorEvents() {
		reported = reported || (e.Stage == "session_durability" && strings.Contains(e.Message, "disk full"))
	}
	if !reported {
		t.Fatalf("startup save failure was not reported: %+v", c.errorEvents())
	}
	c.saveErr = nil
	c.say("Still works.")

	binding, err := c.catalog.ResolveBinding(c.base, "ember")
	if err != nil {
		t.Fatal(err)
	}
	ollama, err := brain.NewProvider(binding.Config())
	if err != nil {
		t.Fatal(err)
	}
	p := &pipeline.Pipeline{Brain: ollama, Events: events.NewBus()}
	t.Cleanup(func() { _ = p.CloseBrains() })
	_, err = NewSelector(SelectionOptions{Base: c.base, Catalog: c.catalog, Pipeline: p, SessionID: c.sess.ID, Initial: binding,
		PersistContexts: func([]brain.Turn, events.PersonaSelection, brain.PersonaContexts) error {
			return errors.New("disk full")
		}})
	if err == nil {
		t.Fatal("a provider that keeps shared history must still refuse to start without a durable selection")
	}
}

func TestClaudeExplicitSelectionHandsOver(t *testing.T) {
	c := newClaudeConversation(t, 20)
	c.say("Note that the vault code is cobalt.")
	if _, err := c.selector.Select(t.Context(), "ember", "ui"); err != nil {
		t.Fatal(err)
	}
	if got := c.selector.Snapshot().PersonaID; got != "ember" {
		t.Fatalf("selection = %q", got)
	}
}

func TestSameResponderSeparatesSessionChangesFromVoiceChanges(t *testing.T) {
	root := t.TempDir()
	catalog := persona.Catalog{Dir: filepath.Join(root, "personas")}
	mira := testProfile("mira", "Mira", "mira")
	mira.Brain.Provider, mira.Brain.Model = "grok", ""
	writeTestProfile(t, catalog.Dir, mira)
	writeTestPrompt(t, filepath.Join(root, "prompts"), "mira", "You are {agent_name}.")
	base := &config.Config{BrainProvider: "grok", GrokModel: "grok-4", PromptsDir: filepath.Join(root, "prompts"), WorkDir: root}
	resolve := func(cfg *config.Config) *persona.SessionBinding {
		b, err := catalog.ResolveBinding(cfg, "mira")
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	live := resolve(base)
	for name, tc := range map[string]struct {
		change func(*config.Config)
		same   bool
	}{
		"voice":     {func(c *config.Config) { c.TTSProvider = "say" }, true},
		"audio":     {func(c *config.Config) { c.VADSilenceDuration = 1.3 }, true},
		"model":     {func(c *config.Config) { c.GrokModel = "grok-5" }, false},
		"directory": {func(c *config.Config) { c.WorkDir = t.TempDir() }, false},
	} {
		next := base.Clone()
		tc.change(next)
		if got := sameResponder(live, resolve(next), "hash", "hash"); got != tc.same {
			t.Errorf("%s change: same responder = %v, want %v", name, got, tc.same)
		}
	}
	if sameResponder(live, live, "hash", "edited") {
		t.Error("a prompt edit kept the responder")
	}
	renamed := testProfile("mira", "Mira Two", "mira")
	renamed.Brain.Provider, renamed.Brain.Model = "grok", ""
	writeTestProfile(t, catalog.Dir, renamed)
	if sameResponder(live, resolve(base), "hash", "hash") {
		t.Error("a name change kept the responder")
	}
}
