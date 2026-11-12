package personaruntime

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
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
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/pipeline"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/session"
)

// ollamaStall in a user line makes the fake Ollama server send a partial
// reply and hold the request until it is stopped.
const ollamaStall = "HOLD ON"

// localConversation is a conversation among personas whose backends are a
// fake Ollama server, answering from a fixed script with live tool routing,
// and optionally Codex. It keeps every request body Ollama was sent.
type localConversation struct {
	t        *testing.T
	root     string
	base     *config.Config
	catalog  persona.Catalog
	store    *session.Store
	sess     *session.Session
	p        *pipeline.Pipeline
	selector *Selector
	codexLog string

	mu     sync.Mutex
	bodies [][]byte
	ready  []events.ResponseReady
	errors []events.Error
	// stalled receives the request held by ollamaStall once its partial reply is sent.
	stalled chan struct{}
}

// newLocalConversation starts on the first of personas. Each is "id" or
// "id=provider"; a bare id is on Ollama.
func newLocalConversation(t *testing.T, maxHistory int, personas ...string) *localConversation {
	t.Helper()
	c := &localConversation{t: t, stalled: make(chan struct{}, 1)}
	c.root, _ = filepath.EvalSymlinks(t.TempDir())
	server := httptest.NewServer(http.HandlerFunc(c.serveOllama))
	t.Cleanup(server.Close)
	c.catalog = persona.Catalog{Dir: filepath.Join(c.root, "personas")}
	promptDir := filepath.Join(c.root, "prompts")
	for _, spec := range personas {
		id, provider, _ := strings.Cut(spec, "=")
		profile := testProfile(id, strings.ToUpper(id[:1])+id[1:], id)
		profile.Brain.Model = ""
		if provider != "" {
			profile.Brain.Provider = provider
		}
		if provider == "codex" && c.codexLog == "" {
			c.codexLog = installCodexSelectorFixture(t)
		}
		writeTestProfile(t, c.catalog.Dir, profile)
		writeTestPrompt(t, promptDir, id, "You are {agent_name}.")
	}
	start, _, _ := strings.Cut(personas[0], "=")
	c.base = &config.Config{ActivePersona: start, BrainProvider: "ollama", OllamaModel: "fixture:latest", OllamaHost: server.URL,
		PromptsDir: promptDir, WorkDir: c.root, MaxHistory: maxHistory, OllamaNumCtx: 8192,
		ContinuityProjectionEnabled: true, ContinuityArchiveEnabled: true}
	c.store = session.NewStore(filepath.Join(c.root, "sessions"))
	c.sess = session.New("ollama", "fixture:latest")
	c.start(start, nil, nil)
	return c
}

// serveOllama answers a request offering the route tool by choosing chat,
// and any other request with a reply numbered by its request.
func (c *localConversation) serveOllama(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.URL.Path != "/api/chat" {
		_, _ = w.Write([]byte(`{"models":[{"name":"fixture:latest"}]}`))
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		c.t.Error(err)
		return
	}
	var req struct {
		Messages []struct{ Role, Content string }
		Tools    []struct{ Function struct{ Name string } }
	}
	if err := json.Unmarshal(body, &req); err != nil {
		c.t.Error(err)
		return
	}
	c.mu.Lock()
	c.bodies = append(c.bodies, body)
	n := len(c.bodies)
	c.mu.Unlock()
	last := req.Messages[len(req.Messages)-1]
	routing := false
	for _, tool := range req.Tools {
		routing = routing || tool.Function.Name == "route"
	}
	enc := json.NewEncoder(w)
	if routing && last.Role != "tool" {
		_ = enc.Encode(map[string]any{"model": "fixture:latest", "done": true, "done_reason": "stop", "message": map[string]any{"role": "assistant", "content": "",
			"tool_calls": []any{map[string]any{"id": fmt.Sprintf("call-%d", n), "function": map[string]any{"name": "route", "arguments": map[string]string{"kind": "chat"}}}}}})
		return
	}
	if strings.Contains(lastUserLine(req.Messages), ollamaStall) {
		_ = enc.Encode(map[string]any{"model": "fixture:latest", "done": false, "message": map[string]string{"role": "assistant", "content": "Let me think about "}})
		w.(http.Flusher).Flush()
		c.stalled <- struct{}{}
		<-r.Context().Done()
		return
	}
	_ = enc.Encode(map[string]any{"model": "fixture:latest", "done": true, "done_reason": "stop", "message": map[string]string{"role": "assistant", "content": fmt.Sprintf("Reply %d.", n)}})
}

func lastUserLine(messages []struct{ Role, Content string }) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "user" {
			return messages[i].Content
		}
	}
	return ""
}

// start binds a fresh pipeline and selector to the conversation, as a host
// does at launch or resume.
func (c *localConversation) start(id string, turns []brain.Turn, contexts brain.PersonaContexts) {
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
	p := &pipeline.Pipeline{Brain: initial, Events: events.NewBus(), VoiceToolsEnabled: true}
	events.Subscribe(p.Events, func(e events.Error) {
		c.mu.Lock()
		c.errors = append(c.errors, e)
		c.mu.Unlock()
	})
	events.Subscribe(p.Events, func(e events.ResponseReady) {
		c.mu.Lock()
		c.ready = append(c.ready, e)
		c.mu.Unlock()
	})
	if err := p.BeginConversationIdentity(c.sess.ID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := p.CloseBrains(); err != nil {
			t.Error(err)
		}
	})
	selector, err := NewSelector(SelectionOptions{Base: c.base, Catalog: c.catalog, Pipeline: p, SessionID: c.sess.ID, Initial: binding,
		Transcript: turns, InitialContexts: contexts, SessionProvider: c.sess.Provider,
		PersistContexts: func(turns []brain.Turn, s events.PersonaSelection, contexts brain.PersonaContexts) error {
			return c.store.SaveSelectionState(c.sess, turns, s, contexts)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(selector.Close)
	p.AdmitInput = selector.Route
	p.HostedNotice, p.LiveHostedNotice = selector.HostedNotice, selector.LiveHostedNotice
	p.OnTurn = func() {
		if err := c.store.Save(c.sess, selector.Transcript(), selector.ContextSnapshots()); err != nil {
			t.Error(err)
		}
	}
	c.p, c.selector = p, selector
}

// resume starts the conversation again from its saved session.
func (c *localConversation) resume() {
	c.t.Helper()
	saved := c.saved()
	c.sess = saved
	c.start(saved.SelectedPersonaID, saved.Turns, saved.PersonaContexts)
}

func (c *localConversation) say(inputs ...string) {
	c.t.Helper()
	ctx, cancel := context.WithTimeout(c.t.Context(), 30*time.Second)
	defer cancel()
	for _, input := range inputs {
		if err := c.p.RunTurnTextMode(ctx, input); err != nil {
			c.t.Fatalf("%q: %v", input, err)
		}
	}
}

// requests returns each body Ollama was sent, with the conversation's
// directory replaced so runs can be compared.
func (c *localConversation) requests() [][]byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([][]byte, len(c.bodies))
	for i, body := range c.bodies {
		out[i] = bytes.ReplaceAll(body, []byte(c.root), []byte("$ROOT"))
	}
	return out
}

func (c *localConversation) saved() *session.Session {
	c.t.Helper()
	loaded, err := c.store.Load(c.sess.ID)
	if err != nil {
		c.t.Fatal(err)
	}
	return loaded
}

func (c *localConversation) errorEvents() []events.Error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]events.Error(nil), c.errors...)
}

// requestLog renders request bodies for a golden file: each body's digest,
// then its messages and tool names, so a mismatch reads as a diff of what the
// model was sent.
func requestLog(t *testing.T, bodies [][]byte) string {
	t.Helper()
	var b strings.Builder
	for i, body := range bodies {
		var req struct {
			Messages []map[string]any
			Tools    []struct{ Function struct{ Name string } }
		}
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(body)
		fmt.Fprintf(&b, "request %d sha256 %s tools", i+1, hex.EncodeToString(sum[:]))
		for _, tool := range req.Tools {
			b.WriteString(" " + tool.Function.Name)
		}
		b.WriteString("\n")
		for _, m := range req.Messages {
			line, _ := json.Marshal(m)
			b.WriteString("  " + string(line) + "\n")
		}
	}
	return b.String()
}

func assertRequestGolden(t *testing.T, name string, bodies [][]byte) {
	t.Helper()
	got := requestLog(t, bodies)
	path := filepath.Join("testdata", name)
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden (run with UPDATE_GOLDEN=1 to create): %v", err)
	}
	if got != string(want) {
		gotLines, wantLines := strings.Split(got, "\n"), strings.Split(string(want), "\n")
		for i := range min(len(gotLines), len(wantLines)) {
			if gotLines[i] != wantLines[i] {
				t.Fatalf("Ollama was sent something else at line %d of %s\n got: %s\nwant: %s", i+1, path, gotLines[i], wantLines[i])
			}
		}
		t.Fatalf("Ollama was sent %d lines of requests, want %d (%s)", len(gotLines), len(wantLines), path)
	}
}

// localConversationScript is a conversation among three personas on Ollama,
// with live tool routing on every turn, long enough to trim the live window,
// and with a switch by name, by @id, and back.
var localConversationScript = []string{
	"Note that the vault code is cobalt.",
	"And the door is teal.",
	"switch to Scout",
	"What do you know so far?",
	"Add that the key is under the mat.",
	"@ember What was added?",
	"Summarize everything.",
	"One more detail: the alarm code is 4512.",
	"switch to Nova",
	"Anything new?",
	"switch to Scout",
	"And now?",
}

func TestLocalConversationSendsOllamaWhatItWasSentBefore(t *testing.T) {
	c := newLocalConversation(t, 6, "ember", "scout", "nova")
	c.say(localConversationScript...)
	if errs := c.errorEvents(); len(errs) > 0 {
		t.Fatalf("errors: %+v", errs)
	}
	assertRequestGolden(t, "local_conversation_requests.golden", c.requests())
}

func TestResumedLocalConversationSendsOllamaWhatItWasSentBefore(t *testing.T) {
	c := newLocalConversation(t, 6, "ember", "scout", "nova")
	c.say(localConversationScript[:7]...)
	c.resume()
	c.say(localConversationScript[7:]...)
	if errs := c.errorEvents(); len(errs) > 0 {
		t.Fatalf("errors: %+v", errs)
	}
	// Resuming changes nothing Ollama is sent.
	assertRequestGolden(t, "local_conversation_requests.golden", c.requests())
}

func TestLocalConversationWithACodexPersonaSendsOllamaWhatItWasSentBefore(t *testing.T) {
	c := newLocalConversation(t, 6, "ember", "mira=codex", "scout")
	c.say("Note that the vault code is cobalt.", "switch to Mira", "What is the vault code?", "switch to Scout", "What was just said?", "switch to Ember", "And now?")
	if errs := c.errorEvents(); len(errs) > 0 {
		t.Fatalf("errors: %+v", errs)
	}
	assertRequestGolden(t, "codex_local_conversation_requests.golden", c.requests())
}
