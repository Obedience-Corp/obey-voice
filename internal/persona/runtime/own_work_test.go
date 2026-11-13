package personaruntime

import (
	"context"
	"encoding/json"
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
)

const (
	// claudeJobBrief opens every background job's brief.
	claudeJobBrief  = "You are running one background task for a voice assistant."
	claudeJobResult = "JOB RESULT: Mira switched herself to haiku."
	ollamaJobResult = "JOB RESULT from"
	privateLine     = "PRIVATE-PREINVITE my PIN is 4711."
)

// fixtureRoute answers a harness routing request: spawn when the latest
// request names OBEY_CLAUDE_FIXTURE_SPAWN, chat otherwise. With worker tiers
// on, the spawn names OBEY_CLAUDE_FIXTURE_TIER.
func fixtureRoute(payload string) string {
	var req struct {
		Latest string `json:"latest_user_request"`
	}
	_ = json.Unmarshal([]byte(payload), &req)
	if marker := os.Getenv("OBEY_CLAUDE_FIXTURE_SPAWN"); marker != "" && strings.Contains(req.Latest, marker) {
		if tier := os.Getenv("OBEY_CLAUDE_FIXTURE_TIER"); tier != "" {
			return `{"kind":"spawn","tier":"` + tier + `"}`
		}
		return `{"kind":"spawn"}`
	}
	return `{"kind":"chat"}`
}

// holdFixtureJob keeps a background job running while the file named by
// OBEY_CLAUDE_FIXTURE_HOLD exists. fixtureHold removes it on release, and
// the test's temporary directory takes it with it when the test ends.
func holdFixtureJob() {
	path := os.Getenv("OBEY_CLAUDE_FIXTURE_HOLD")
	if path == "" {
		return
	}
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err != nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// fixtureHold holds the fake claude's background jobs until the returned
// release is called or the test ends, whichever comes first.
func fixtureHold(t *testing.T) (release func()) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "hold")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OBEY_CLAUDE_FIXTURE_HOLD", path)
	release = func() {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			t.Error(err)
		}
	}
	t.Cleanup(release)
	return release
}

// ownWorkSetup turns on routing and background jobs, with the configured
// worker on the fake Ollama, and binds Mira on Claude to model haiku, her own
// directory and the given work setting.
func ownWorkSetup(t *testing.T, work, dir string) func(*claudeConversation) {
	return func(c *claudeConversation) {
		c.jobs, c.tools = true, true
		c.base.VoiceToolsEnabled = true
		c.base.VoiceWorkerProvider, c.base.VoiceWorkerModel = "ollama", "fixture:latest"
		mira := testProfile("mira", "Mira", "mira")
		mira.Brain = persona.Brain{Provider: "claude", Model: "haiku", Work: work}
		mira.StartingDirectory = dir
		writeTestProfile(t, c.catalog.Dir, mira)
	}
}

func awaitWorker(t *testing.T, p *pipeline.Pipeline, state string) pipeline.WorkerInfo {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		for _, w := range p.Workers() {
			if w.State == state {
				return w
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("no job %s: %+v", state, p.Workers())
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func jobRequests(t *testing.T, log string) (jobs, live []claudeRequest) {
	t.Helper()
	for _, req := range readClaudeRequests(t, log) {
		switch {
		case strings.Contains(req.Prompt, claudeJobBrief):
			jobs = append(jobs, req)
		case req.Format == "stream-json":
			live = append(live, req)
		}
	}
	return jobs, live
}

func TestPersonaOnClaudeDoesItsOwnWorkWhileTheRoomTalks(t *testing.T) {
	t.Setenv("OBEY_CLAUDE_FIXTURE_SESSIONS", "distinct")
	t.Setenv("OBEY_CLAUDE_FIXTURE_SPAWN", "haiku")
	miraDir, _ := filepath.EvalSymlinks(t.TempDir())
	c := newConversation(t, 50, "ember", "claude", "", ownWorkSetup(t, "", miraDir))
	release := fixtureHold(t)
	finished := make(chan events.JobFinished, 1)
	events.Subscribe(c.p.Events, func(e events.JobFinished) { finished <- e })

	c.say(privateLine, "Mira, hello there.")
	// Ember's tool traffic, recorded while Mira holds the conversation.
	if err := c.selector.transcript.Append(c.p.ConversationScope().ConversationEpoch,
		brain.Turn{Role: "assistant", Content: "Checking the logs.", PersonaID: "ember", PersonaDisplayName: "Ember",
			ToolCalls: []brain.TurnToolCall{{ID: "call-9", Name: "read_file", Arguments: `{"path":"TOOLCALLSECRET"}`}}},
		brain.Turn{Role: "tool", ToolName: "read_file", ToolCallID: "call-9", Content: "TOOLRESULTSECRET"},
	); err != nil {
		t.Fatal(err)
	}
	c.say("Mira, did you catch that?")
	_, live := jobRequests(t, c.log)
	miraSession := live[len(live)-1].Session

	c.say("Mira, can you change it to haiku?")
	running := awaitWorker(t, c.p, "running")
	if running.Provider != "claude" || running.Model != "haiku" || running.PersonaID != "mira" {
		t.Fatalf("job = %+v, want Mira's own claude/haiku", running)
	}

	// The room keeps talking while Mira works.
	before := len(c.responses())
	c.say("Ember, what do you think?")
	if got := c.responses(); len(got) != before+1 || got[len(got)-1].Envelope == nil || got[len(got)-1].Envelope.PersonaID != "ember" {
		t.Fatalf("Ember did not answer while the job ran: %+v", got[before:])
	}
	if w := awaitWorker(t, c.p, "running"); w.ID != running.ID {
		t.Fatalf("job %s is not the one still running", w.ID)
	}

	release()
	var done events.JobFinished
	select {
	case done = <-finished:
	case <-time.After(10 * time.Second):
		t.Fatalf("job did not finish: %+v", c.p.Workers())
	}
	if done.Summary != claudeJobResult || done.Envelope == nil || done.Envelope.PersonaID != "mira" || done.Envelope.DisplayName != "Mira" || done.Envelope.Voice != "mira-voice" {
		t.Fatalf("result = %+v, want Mira's result under her name and voice", done)
	}
	// The next pause records it as Mira's line.
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	if err := c.p.RunBackgroundTurn(ctx); err != nil {
		t.Fatal(err)
	}
	recorded := false
	for _, turn := range c.selector.Transcript() {
		if turn.Role == "assistant" && turn.Content == claudeJobResult {
			recorded = turn.PersonaID == "mira" && turn.PersonaDisplayName == "Mira"
		}
	}
	if !recorded {
		t.Fatalf("job result not recorded as Mira's line: %+v", replies(c.selector.Transcript()))
	}

	c.say("Mira, are you done?")
	jobs, live := jobRequests(t, c.log)
	if len(jobs) != 1 {
		t.Fatalf("job requests = %d, want 1", len(jobs))
	}
	job := jobs[0]
	if !job.NoPersist || job.Resume != "" || job.Session == miraSession || job.Model != "haiku" || job.Permission != "bypassPermissions" || job.Cwd != miraDir {
		t.Fatalf("job ran as resume=%q session=%q (live %q) model=%q permission=%q no-persist=%v cwd=%q, want a fresh unsaved haiku session in %q",
			job.Resume, job.Session, miraSession, job.Model, job.Permission, job.NoPersist, job.Cwd, miraDir)
	}
	if !strings.Contains(job.Prompt, "Mira, hello there.") || !strings.Contains(job.Prompt, "change it to haiku") {
		t.Fatalf("job brief lacks Mira's share:\n%s", job.Prompt)
	}
	for _, req := range jobs {
		sent := req.Prompt + "\x00" + strings.Join(req.Args, "\x00")
		for _, secret := range []string{"PRIVATE-PREINVITE", "4711", "TOOLCALLSECRET", "TOOLRESULTSECRET"} {
			if strings.Contains(sent, secret) {
				t.Fatalf("job request carries %q", secret)
			}
		}
	}
	last := live[len(live)-1]
	if last.Resume != miraSession || last.Session != miraSession || last.NoPersist || last.Model != "haiku" {
		t.Fatalf("Mira's talking session changed: resume=%q session=%q (was %q) no-persist=%v model=%q", last.Resume, last.Session, miraSession, last.NoPersist, last.Model)
	}
	if !strings.Contains(last.Prompt, claudeJobResult) {
		t.Fatalf("Mira's session was not told her own result:\n%s", last.Prompt)
	}
	for _, msg := range c.ollamaRequests() {
		for _, m := range msg {
			if strings.Contains(m.Content, claudeJobBrief) {
				t.Fatal("the configured Ollama worker received Mira's job")
			}
		}
	}
}

func TestPersonaOnClaudeSetToWorkerHandsItsWorkToTheConfiguredWorker(t *testing.T) {
	t.Setenv("OBEY_CLAUDE_FIXTURE_SPAWN", "haiku")
	miraDir, _ := filepath.EvalSymlinks(t.TempDir())
	c := newConversation(t, 50, "mira", "claude", "", ownWorkSetup(t, config.WorkWorker, miraDir))
	finished := make(chan events.JobFinished, 1)
	events.Subscribe(c.p.Events, func(e events.JobFinished) { finished <- e })
	c.say("Mira, can you change it to haiku?")
	select {
	case done := <-finished:
		if !strings.HasPrefix(done.Summary, ollamaJobResult) || done.Envelope == nil || done.Envelope.PersonaID != "mira" {
			t.Fatalf("result = %+v, want the configured worker's answer under Mira", done)
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("job did not finish: %+v", c.p.Workers())
	}
	if jobs, _ := jobRequests(t, c.log); len(jobs) != 0 {
		t.Fatalf("Mira's own backend ran %d job request(s)", len(jobs))
	}
	if w := c.p.Workers(); len(w) != 1 || w[0].Provider != "ollama" || w[0].Model != "fixture:latest" {
		t.Fatalf("job = %+v, want the configured Ollama worker", w)
	}
}

func TestPersonaOnOllamaDoesItsOwnWorkOnItsOwnModel(t *testing.T) {
	for _, tc := range []struct {
		work   string
		numCtx int
	}{
		{config.WorkOwn, 8192},
		{"", 65536},
	} {
		t.Run("work="+tc.work, func(t *testing.T) {
			c := newConversation(t, 50, "ember", "claude", "", func(c *claudeConversation) {
				ownWorkSetup(t, "", t.TempDir())(c)
				c.base.VoiceWorkerNumCtx = 65536
				ember := testProfile("ember", "Ember", "ember")
				ember.Brain = persona.Brain{Provider: "ollama", Work: tc.work}
				writeTestProfile(t, c.catalog.Dir, ember)
			})
			finished := make(chan events.JobFinished, 1)
			events.Subscribe(c.p.Events, func(e events.JobFinished) { finished <- e })
			c.say("look up the release notes")
			select {
			case done := <-finished:
				if !strings.HasPrefix(done.Summary, ollamaJobResult) || done.Envelope == nil || done.Envelope.PersonaID != "ember" {
					t.Fatalf("result = %+v", done)
				}
			case <-time.After(10 * time.Second):
				t.Fatalf("job did not finish: %+v", c.p.Workers())
			}
			c.mu.Lock()
			calls := append([]ollamaCall(nil), c.ollamaCalls...)
			c.mu.Unlock()
			var job *ollamaCall
			for i := range calls {
				for _, m := range calls[i].Messages {
					if strings.Contains(m.Content, claudeJobBrief) {
						job = &calls[i]
					}
				}
			}
			if job == nil || job.Model != "fixture:latest" || job.NumCtx != tc.numCtx {
				t.Fatalf("job request = %+v, want fixture:latest with num_ctx %d", job, tc.numCtx)
			}
		})
	}
}

// An edit to brain.work reaches the conversation when the persona is next
// selected, and does not restart its talking session.
func TestWorkEditAppliesOnTheNextSelectionWithoutRestartingTheSession(t *testing.T) {
	t.Setenv("OBEY_CLAUDE_FIXTURE_SESSIONS", "distinct")
	t.Setenv("OBEY_CLAUDE_FIXTURE_SPAWN", "haiku")
	miraDir, _ := filepath.EvalSymlinks(t.TempDir())
	c := newConversation(t, 50, "mira", "claude", "", ownWorkSetup(t, "", miraDir))
	finished := make(chan events.JobFinished, 1)
	events.Subscribe(c.p.Events, func(e events.JobFinished) { finished <- e })

	c.say("Mira, hello there.")
	_, live := jobRequests(t, c.log)
	miraSession := live[len(live)-1].Session
	c.say("Mira, can you change it to haiku?")
	select {
	case done := <-finished:
		if done.Summary != claudeJobResult {
			t.Fatalf("result = %+v, want Mira's own", done)
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("job did not finish: %+v", c.p.Workers())
	}

	mira := testProfile("mira", "Mira", "mira")
	mira.Brain = persona.Brain{Provider: "claude", Model: "haiku", Work: config.WorkWorker}
	mira.StartingDirectory = miraDir
	writeTestProfile(t, c.catalog.Dir, mira)
	c.say("Ember, what do you think?", "Mira, can you change it to haiku?")
	select {
	case done := <-finished:
		if !strings.HasPrefix(done.Summary, ollamaJobResult) {
			t.Fatalf("result = %+v, want the configured worker's", done)
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("job did not finish: %+v", c.p.Workers())
	}
	c.say("Mira, thanks.")
	jobs, live := jobRequests(t, c.log)
	if len(jobs) != 1 {
		t.Fatalf("Mira's own backend ran %d job request(s), want only the one before she was set to worker", len(jobs))
	}
	if last := live[len(live)-1]; last.Resume != miraSession {
		t.Fatalf("Mira's session restarted: resume %q, was %q", last.Resume, miraSession)
	}
}

// A persona on Ollama set to do its own work needs no configured worker: the
// model's own route call spawns the job, and it runs on the persona's model.
func TestPersonaOnOllamaDoesItsOwnWorkWithNoConfiguredWorker(t *testing.T) {
	t.Setenv("OBEY_OLLAMA_FIXTURE_SPAWN", "tidy the notes")
	c := newConversation(t, 50, "ember", "claude", "", func(c *claudeConversation) {
		ownWorkSetup(t, "", t.TempDir())(c)
		c.base.VoiceWorkerProvider = "bogus"
		ember := testProfile("ember", "Ember", "ember")
		ember.Brain = persona.Brain{Provider: "ollama", Work: config.WorkOwn}
		writeTestProfile(t, c.catalog.Dir, ember)
	})
	if _, err := brain.NewWorkerProvider(c.base); err == nil {
		t.Fatal("the configured worker can be built; the test needs it missing")
	}
	finished := make(chan events.JobFinished, 1)
	events.Subscribe(c.p.Events, func(e events.JobFinished) { finished <- e })
	var routes []pipeline.RouteDecision
	c.p.OnRoute = func(d pipeline.RouteDecision) { routes = append(routes, d) }
	c.say("Please tidy the notes.")
	select {
	case done := <-finished:
		if !strings.HasPrefix(done.Summary, ollamaJobResult+" fixture:latest") || done.Envelope == nil || done.Envelope.PersonaID != "ember" {
			t.Fatalf("result = %+v", done)
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("job did not finish: %+v errors %+v", c.p.Workers(), c.errorEvents())
	}
	if len(routes) != 1 || routes[0].Kind != pipeline.RouteSpawn || routes[0].Source != "router" {
		t.Fatalf("routes = %+v, want the model's spawn", routes)
	}
	if w := c.p.Workers(); len(w) != 1 || w[0].Provider != "ollama" || w[0].Model != "fixture:latest" || w[0].PersonaID != "ember" {
		t.Fatalf("job = %+v", w)
	}
	if errs := c.errorEvents(); len(errs) != 0 {
		t.Fatalf("errors = %+v", errs)
	}
}

// In tiered mode with voice_tools_enabled off, a persona on Claude still
// routes work, and its own job runs the CLI with every tool off.
func TestPersonaOnClaudeInTieredModeDoesItsOwnWorkWithToolsOff(t *testing.T) {
	t.Setenv("OBEY_CLAUDE_FIXTURE_SPAWN", "haiku")
	t.Setenv("OBEY_CLAUDE_FIXTURE_TIER", "simple")
	miraDir, _ := filepath.EvalSymlinks(t.TempDir())
	c := newConversation(t, 50, "mira", "claude", "", func(c *claudeConversation) {
		ownWorkSetup(t, "", miraDir)(c)
		c.tools, c.base.VoiceToolsEnabled = false, false
		tiers := config.DefaultVoiceWorkers()
		tiers.Mode = config.WorkerModeTiered
		host := "http://127.0.0.1:11434"
		tiers.Tiers.Simple.Enabled = true
		tiers.Tiers.Simple.Assignment = &config.WorkerAssignment{Provider: "ollama", Model: "fixture:latest", EndpointRef: &host, Billing: config.WorkerBillingLocal}
		c.base.VoiceWorkers = tiers
	})
	finished := make(chan events.JobFinished, 1)
	events.Subscribe(c.p.Events, func(e events.JobFinished) { finished <- e })
	c.say("Mira, can you change it to haiku?")
	select {
	case done := <-finished:
		if done.Summary != claudeJobResult {
			t.Fatalf("result = %+v", done)
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("job did not finish: %+v errors %+v", c.p.Workers(), c.errorEvents())
	}
	jobs, _ := jobRequests(t, c.log)
	if len(jobs) != 1 {
		t.Fatalf("job requests = %d", len(jobs))
	}
	job := jobs[0]
	toolsOff := false
	for i, arg := range job.Args {
		if arg == "--tools" && i+1 < len(job.Args) && job.Args[i+1] == "" {
			toolsOff = true
		}
	}
	if !toolsOff || job.Permission == "bypassPermissions" || !job.NoPersist || job.Model != "haiku" || job.Cwd != miraDir {
		t.Fatalf("own job ran with tools off %v, permission %q, no-persist %v, model %q, cwd %q; args %q", toolsOff, job.Permission, job.NoPersist, job.Model, job.Cwd, job.Args)
	}
}

// A worker chosen only in Settings leaves a persona on Claude doing its own
// work, and says so once.
func TestAWorkerChosenOnlyInSettingsLeavesAPersonaOnClaudeDoingItsOwnWork(t *testing.T) {
	t.Setenv("OBEY_CLAUDE_FIXTURE_SPAWN", "haiku")
	miraDir, _ := filepath.EvalSymlinks(t.TempDir())
	c := newConversation(t, 50, "mira", "claude", "", ownWorkSetup(t, "", miraDir))
	if !config.WorkerSelected(c.base) {
		t.Fatal("the test needs a worker chosen in Settings")
	}
	finished := make(chan events.JobFinished, 2)
	events.Subscribe(c.p.Events, func(e events.JobFinished) { finished <- e })
	var mu sync.Mutex
	var notices []string
	events.Subscribe(c.p.Events, func(e events.Info) {
		if strings.Contains(e.Message, "not on the worker chosen in Settings") {
			mu.Lock()
			notices = append(notices, e.Message)
			mu.Unlock()
		}
	})
	for range 2 {
		c.say("Mira, can you change it to haiku?")
		select {
		case done := <-finished:
			if done.Summary != claudeJobResult {
				t.Fatalf("result = %+v, want Mira's own", done)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("job did not finish: %+v", c.p.Workers())
		}
	}
	if jobs, _ := jobRequests(t, c.log); len(jobs) != 2 {
		t.Fatalf("Mira's own backend ran %d job(s), want 2", len(jobs))
	}
	for _, msg := range c.ollamaRequests() {
		for _, m := range msg {
			if strings.Contains(m.Content, claudeJobBrief) {
				t.Fatal("the worker chosen in Settings received Mira's job")
			}
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(notices) != 1 || !strings.Contains(notices[0], "obey-voice persona edit mira --work worker") {
		t.Fatalf("notices = %q, want one", notices)
	}
}
