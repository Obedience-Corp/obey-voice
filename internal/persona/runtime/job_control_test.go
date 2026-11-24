package personaruntime

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
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

// Lines the fake model answers with a tool call: a hand-off to a worker, and
// a cancel of the first job.
const (
	handOffWork = "Write five numbered files."
	cancelJob   = "Cancel job one."
)

// idleWorker is a background job with no model behind it: it runs until it
// is cancelled.
type idleWorker struct{}

func (idleWorker) ThinkStream(context.Context, string, brain.StreamOptions) (*brain.Stream, error) {
	return nil, context.Canceled
}

func (idleWorker) ThinkFull(ctx context.Context, _ string, _ brain.StreamOptions) (string, error) {
	<-ctx.Done()
	return "", ctx.Err()
}

func (idleWorker) ClearHistory()            {}
func (idleWorker) History() []brain.Turn    { return nil }
func (idleWorker) LoadHistory([]brain.Turn) {}

// controlConversation is Ollama personas on the direct router under the real
// selector, with an agency whose jobs run on idleWorker.
type controlConversation struct {
	t        *testing.T
	p        *pipeline.Pipeline
	selector *Selector

	mu       sync.Mutex
	offered  []bool
	ready    []string
	controls []string
}

// newControlConversation starts the first of personas; global is the
// installation's voice_job_control and settings each persona's
// brain.job_control (nil inherits).
func newControlConversation(t *testing.T, global bool, settings map[string]*bool, personas ...string) *controlConversation {
	t.Helper()
	c := &controlConversation{t: t}
	root, _ := filepath.EvalSymlinks(t.TempDir())
	config.SetConfigDirForTest(t, t.TempDir())
	server := httptest.NewServer(http.HandlerFunc(c.serve))
	t.Cleanup(server.Close)
	catalog := persona.Catalog{Dir: filepath.Join(root, "personas")}
	promptDir := filepath.Join(root, "prompts")
	for _, id := range personas {
		profile := testProfile(id, strings.ToUpper(id[:1])+id[1:], id)
		profile.Brain.Model = ""
		profile.Brain.JobControl = settings[id]
		writeTestProfile(t, catalog.Dir, profile)
		writeTestPrompt(t, promptDir, id, "You are {agent_name}.")
	}
	base := &config.Config{ActivePersona: personas[0], BrainProvider: "ollama", OllamaModel: "fixture:latest", OllamaHost: server.URL,
		VoiceRouterTools: "direct", VoiceJobControl: global, PromptsDir: promptDir, WorkDir: root, MaxHistory: 40, OllamaNumCtx: 8192,
		ContinuityProjectionEnabled: true, ContinuityArchiveEnabled: true}
	binding, err := catalog.ResolveBinding(base, personas[0])
	if err != nil {
		t.Fatal(err)
	}
	initial, err := brain.NewProvider(binding.Config())
	if err != nil {
		t.Fatal(err)
	}
	p := &pipeline.Pipeline{Brain: initial, Events: events.NewBus(), VoiceToolsEnabled: true, RouterToolsPolicy: brain.RouterToolsDirect}
	jobs := job.NewTable(t.Context())
	t.Cleanup(jobs.Close)
	pipeline.NewAgency(p, jobs, idleWorker{}, binding.Config(), nil).Wire()
	sess := session.New("ollama", "fixture:latest")
	if err := p.BeginConversationIdentity(sess.ID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.CloseBrains() })
	events.Subscribe(p.Events, func(e events.ResponseReady) {
		c.mu.Lock()
		c.ready = append(c.ready, e.Response)
		c.mu.Unlock()
	})
	events.Subscribe(p.Events, func(e events.RouteDecided) {
		if e.Kind == "job.control" {
			c.mu.Lock()
			c.controls = append(c.controls, e.State)
			c.mu.Unlock()
		}
	})
	store := session.NewStore(filepath.Join(root, "sessions"))
	selector, err := NewSelector(SelectionOptions{Base: base, Catalog: catalog, Pipeline: p, SessionID: sess.ID, Initial: binding,
		SessionProvider: sess.Provider,
		NewWorker:       func(context.Context, *config.Config) (brain.Provider, error) { return idleWorker{}, nil },
		PersistContexts: func(turns []brain.Turn, s events.PersonaSelection, contexts brain.PersonaContexts) error {
			return store.SaveSelectionState(sess, turns, s, contexts)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(selector.Close)
	p.AdmitInput = selector.Route
	c.p, c.selector = p, selector
	return c
}

// serve answers the human's line: a hand-off, a cancel of job-1, or a
// reply. It records whether each request offered job_control.
func (c *controlConversation) serve(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	switch r.URL.Path {
	case "/api/show":
		_, _ = w.Write([]byte(`{"capabilities":["completion","tools"]}`))
		return
	case "/api/chat":
	default:
		_, _ = w.Write([]byte(`{"models":[{"name":"fixture:latest"}]}`))
		return
	}
	var req struct {
		Messages []struct{ Role, Content string }
		Tools    []struct{ Function struct{ Name string } }
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		c.t.Error(err)
		return
	}
	offered := false
	for _, tool := range req.Tools {
		offered = offered || tool.Function.Name == "job_control"
	}
	c.mu.Lock()
	c.offered = append(c.offered, offered)
	c.mu.Unlock()
	reply := map[string]any{"role": "assistant", "content": "Okay."}
	call := func(name string, args map[string]string) {
		reply = map[string]any{"role": "assistant", "content": "", "tool_calls": []any{map[string]any{"id": "call-1", "function": map[string]any{"name": name, "arguments": args}}}}
	}
	switch line := lastUserLine(req.Messages); {
	case strings.Contains(line, handOffWork):
		call("start_background_work", map[string]string{})
	case strings.Contains(line, cancelJob):
		call("job_control", map[string]string{"action": "cancel", "job_id": "job-1"})
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"model": "fixture:latest", "done": true, "done_reason": "stop", "message": reply})
}

func (c *controlConversation) say(input string) {
	c.t.Helper()
	ctx, cancel := context.WithTimeout(c.t.Context(), 30*time.Second)
	defer cancel()
	if err := c.p.RunTurnTextMode(ctx, input); err != nil {
		c.t.Fatalf("%q: %v", input, err)
	}
}

// lastOffered reports whether the newest request offered job_control.
func (c *controlConversation) lastOffered() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.offered) > 0 && c.offered[len(c.offered)-1]
}

func (c *controlConversation) lastReply() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.ready) == 0 {
		return ""
	}
	return c.ready[len(c.ready)-1]
}

func (c *controlConversation) jobState(id string) string {
	for _, w := range c.p.Workers() {
		if w.ID == id {
			return w.State
		}
	}
	return ""
}

// handOff has the live persona start job-1 through its own hand-off.
func (c *controlConversation) handOff() {
	c.t.Helper()
	c.say(handOffWork)
	if state := c.jobState("job-1"); state != "running" {
		c.t.Fatalf("job-1 after the hand-off is %q; workers %+v", state, c.p.Workers())
	}
}

// Turned on for one persona while the installation has it off, the tool is in
// that persona's request and its cancel of the job it started ends the job.
func TestPersonaJobControlOnWhileTheInstallationIsOff(t *testing.T) {
	c := newControlConversation(t, false, map[string]*bool{"robin": new(true)}, "robin")
	c.handOff()
	if !c.lastOffered() {
		t.Fatal("the persona's request did not offer job_control")
	}
	c.say(cancelJob)
	if state := c.jobState("job-1"); state != "aborted" || !strings.Contains(c.lastReply(), "Stopped") {
		t.Fatalf("job-1 is %q, reply %q", state, c.lastReply())
	}
}

// Turned off for one persona while the installation has it on, the tool is
// not in its request and a stray call changes nothing.
func TestPersonaJobControlOffWhileTheInstallationIsOn(t *testing.T) {
	c := newControlConversation(t, true, map[string]*bool{"robin": new(false)}, "robin")
	c.handOff()
	if c.lastOffered() {
		t.Fatal("the persona's request offered job_control")
	}
	c.say(cancelJob)
	if state := c.jobState("job-1"); state != "running" || c.lastReply() != "I couldn't do that." {
		t.Fatalf("job-1 is %q, reply %q", state, c.lastReply())
	}
}

// Switching between a persona with job control and one without changes the
// offer and the grant together, and a persona cannot end another's job.
func TestJobControlFollowsTheSelectedPersona(t *testing.T) {
	c := newControlConversation(t, false, map[string]*bool{"robin": new(true), "sage": new(true), "wren": nil}, "robin", "sage", "wren")
	c.handOff()

	c.say("switch to Wren")
	c.say(cancelJob)
	if c.lastOffered() || c.jobState("job-1") != "running" || c.lastReply() != "I couldn't do that." {
		t.Fatalf("wren: offered %t, job-1 %q, reply %q", c.lastOffered(), c.jobState("job-1"), c.lastReply())
	}

	c.say("switch to Sage")
	c.say(cancelJob)
	if !c.lastOffered() || c.jobState("job-1") != "running" || c.lastReply() != "I can only do that for my own jobs." {
		t.Fatalf("sage: offered %t, job-1 %q, reply %q", c.lastOffered(), c.jobState("job-1"), c.lastReply())
	}

	c.say("switch to Robin")
	c.say(cancelJob)
	if !c.lastOffered() || c.jobState("job-1") != "aborted" {
		t.Fatalf("robin: offered %t, job-1 %q, reply %q", c.lastOffered(), c.jobState("job-1"), c.lastReply())
	}
}

// The default persona, which the installation does not name, owns the job it
// hands off.
func TestDefaultPersonaControlsTheJobItHandedOff(t *testing.T) {
	c := newControlConversation(t, true, nil, persona.DefaultID)
	c.handOff()
	c.say(cancelJob)
	if state := c.jobState("job-1"); state != "aborted" || !slices.Contains(c.controls, "handled") {
		t.Fatalf("job-1 is %q, reply %q, controls %v", state, c.lastReply(), c.controls)
	}
}
