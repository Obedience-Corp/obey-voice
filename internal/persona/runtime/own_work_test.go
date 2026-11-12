package personaruntime

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
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
// request names OBEY_CLAUDE_FIXTURE_SPAWN, chat otherwise.
func fixtureRoute(payload string) string {
	var req struct {
		Latest string `json:"latest_user_request"`
	}
	_ = json.Unmarshal([]byte(payload), &req)
	if marker := os.Getenv("OBEY_CLAUDE_FIXTURE_SPAWN"); marker != "" && strings.Contains(req.Latest, marker) {
		return `{"kind":"spawn"}`
	}
	return `{"kind":"chat"}`
}

// holdFixtureJob keeps a background job running until the file named by
// OBEY_CLAUDE_FIXTURE_HOLD exists.
func holdFixtureJob() {
	path := os.Getenv("OBEY_CLAUDE_FIXTURE_HOLD")
	if path == "" {
		return
	}
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
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
	hold := filepath.Join(t.TempDir(), "release")
	t.Setenv("OBEY_CLAUDE_FIXTURE_SESSIONS", "distinct")
	t.Setenv("OBEY_CLAUDE_FIXTURE_SPAWN", "haiku")
	t.Setenv("OBEY_CLAUDE_FIXTURE_HOLD", hold)
	miraDir, _ := filepath.EvalSymlinks(t.TempDir())
	c := newConversation(t, 50, "ember", "claude", "", ownWorkSetup(t, "", miraDir))
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

	if err := os.WriteFile(hold, nil, 0o600); err != nil {
		t.Fatal(err)
	}
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
			admission := c.p.Workers()[0]
			if admission.Provider != "ollama" {
				t.Fatalf("job = %+v", admission)
			}
		})
	}
}
