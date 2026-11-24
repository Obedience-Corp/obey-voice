package pipeline

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ollama/ollama/api"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/job"
)

// fixtureJobWorker is a background job with no model behind it: it reports
// its activity (one folder listing unless activity says otherwise), takes
// corrections, and returns only when its job is cancelled or it is released.
// A stubborn one ignores the cancel until released.
type fixtureJobWorker struct {
	release  <-chan struct{}
	stubborn bool
	activity func(brief string) []brain.ToolCall
}

func (w fixtureJobWorker) calls(brief string) []brain.ToolCall {
	if w.activity != nil {
		return w.activity(brief)
	}
	return []brain.ToolCall{{ID: "call_1", Name: "list_files", Kind: brain.KindListDir, Summary: "."}}
}

func (fixtureJobWorker) ReadsWorkerControl() bool { return true }

func (w fixtureJobWorker) ThinkStream(ctx context.Context, input string, opts brain.StreamOptions) (*brain.Stream, error) {
	return (&fakeBrain{chunks: []string{"done"}}).ThinkStream(ctx, input, opts)
}

func (w fixtureJobWorker) ThinkFull(ctx context.Context, brief string, opts brain.StreamOptions) (string, error) {
	for _, call := range w.calls(brief) {
		if opts.OnToolStart != nil {
			opts.OnToolStart(call)
		}
		if opts.OnToolEnd != nil {
			opts.OnToolEnd(brain.ToolResult{ToolCall: call, Preview: "ok"})
		}
	}
	if w.stubborn {
		<-w.release
		return "", context.Canceled
	}
	select {
	case <-w.release:
		return "done", nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func (fixtureJobWorker) ClearHistory()            {}
func (fixtureJobWorker) History() []brain.Turn    { return nil }
func (fixtureJobWorker) LoadHistory([]brain.Turn) {}

// personaControlHarness is a live Ollama persona on the direct router, served
// by a fake Ollama that scripts its replies from the human's words only, with
// a real agency, job table and pipeline. Speech goes to a fake engine.
type personaControlHarness struct {
	t       *testing.T
	p       *Pipeline
	a       *Agency
	live    *brain.OllamaBrain
	cfg     *config.Config
	tts     *progressiveFakeTTS
	release chan struct{}
	worker   fixtureJobWorker
	jobTool  chan string
	finished chan string
	// toolsSeen counts each job's tool calls, read by the test goroutine only.
	toolsSeen map[string]int

	mu       sync.Mutex
	chats    int
	tools    [][]string
	script   func(human string) []api.ChatResponse
	before   func()
	notices  []string
	outcomes []controlOutcome
	live0    int
}

type harnessOption func(*config.Config)

func newPersonaControlHarness(t *testing.T, opts ...harnessOption) *personaControlHarness {
	t.Helper()
	config.SetConfigDirForTest(t, t.TempDir())
	h := &personaControlHarness{t: t, tts: &progressiveFakeTTS{}, release: make(chan struct{}), jobTool: make(chan string, 256), finished: make(chan string, 64), toolsSeen: map[string]int{}}
	t.Cleanup(func() {
		select {
		case <-h.release:
		default:
			close(h.release)
		}
	})
	h.worker = fixtureJobWorker{release: h.release}
	srv := httptest.NewServer(http.HandlerFunc(h.serve))
	t.Cleanup(srv.Close)
	h.cfg = &config.Config{
		OllamaHost: srv.URL, OllamaModel: "fixture", AgentName: "Robin", ActivePersona: "robin",
		PromptsDir: t.TempDir(), WorkDir: t.TempDir(), MaxHistory: 40,
		VoiceToolsEnabled: true, VoiceRouterTools: "direct", VoiceJobControl: true,
		PersonaWork: config.WorkWorker,
	}
	for _, opt := range opts {
		opt(h.cfg)
	}
	live, err := brain.NewOllama(h.cfg.Clone())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = live.Close() })
	h.live = live
	player := newFakePlayer(time.Millisecond)
	t.Cleanup(func() { _ = player.Close() })
	bus := events.NewBus()
	h.p = &Pipeline{Brain: live, TTS: h.tts, Player: player, Events: bus, VoiceToolsEnabled: true, RouterToolsPolicy: brain.NormalizeRouterToolsPolicy(h.cfg.VoiceRouterTools)}
	jobs := job.NewTable(context.Background(), job.WithMaxConcurrent(1))
	t.Cleanup(jobs.Close)
	h.a = NewAgency(h.p, jobs, h.worker, h.cfg.Clone(), nil)
	h.a.Wire()
	h.a.cancelExitWait = 500 * time.Millisecond
	if err := h.p.BeginConversationIdentity("control-session"); err != nil {
		t.Fatal(err)
	}
	h.a.onJobControl = func(o controlOutcome) {
		h.mu.Lock()
		h.outcomes = append(h.outcomes, o)
		h.mu.Unlock()
	}
	events.Subscribe(bus, func(e events.Info) {
		h.mu.Lock()
		h.notices = append(h.notices, e.Message)
		h.mu.Unlock()
	})
	events.Subscribe(bus, func(e events.JobFinished) { h.finished <- e.ID })
	events.Subscribe(bus, func(e events.ToolCallStarted) {
		if e.JobID != "" {
			h.jobTool <- e.JobID
			return
		}
		h.mu.Lock()
		h.live0++
		h.mu.Unlock()
	})
	return h
}

func (h *personaControlHarness) serve(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/api/tags":
		_, _ = io.WriteString(w, `{"models":[{"name":"fixture"}]}`)
		return
	case "/api/show":
		_, _ = io.WriteString(w, `{"capabilities":["completion","tools","vision"],"details":{"family":"fixture"}}`)
		return
	}
	var req api.ChatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.t.Error(err)
		return
	}
	h.mu.Lock()
	h.chats++
	n := h.chats
	var names []string
	for _, tool := range req.Tools {
		names = append(names, tool.Function.Name)
	}
	h.tools = append(h.tools, names)
	script, before := h.script, h.before
	h.before = nil
	h.mu.Unlock()
	if before != nil {
		before()
	}
	var reply []api.ChatResponse
	if script != nil {
		reply = script(humanLine(req, n))
	}
	if reply == nil {
		reply = []api.ChatResponse{{Message: api.Message{Role: "assistant", Content: "Okay."}, Done: true, DoneReason: "stop"}}
	}
	enc := json.NewEncoder(w)
	for _, line := range reply {
		_ = enc.Encode(line)
	}
}

// humanLine is what the person said in a request: the first line of its
// newest user message, before the host's job list and instructions. A later
// round of the same turn is marked with its number.
func humanLine(req api.ChatRequest, n int) string {
	for i := len(req.Messages) - 1; i >= 0; i-- {
		m := req.Messages[i]
		if m.Role == "tool" {
			return "#tool-result"
		}
		if m.Role == "user" {
			line, _, _ := strings.Cut(m.Content, "\n")
			return strings.TrimSpace(line)
		}
	}
	return ""
}

// spawnAs starts a fixture job as persona and returns its id once it is
// queued, or running with all its activity reported.
func (h *personaControlHarness) spawnAs(persona, task string) string {
	return h.spawnNamed(persona, "", task)
}

func (h *personaControlHarness) spawnNamed(persona, display, task string) string {
	h.t.Helper()
	cfg := h.cfg.Clone()
	cfg.ActivePersona = persona
	h.a.ReplaceWorker(h.worker, cfg, nil)
	defer h.a.ReplaceWorker(h.worker, h.cfg.Clone(), nil)
	before := map[string]bool{}
	for _, info := range h.a.jobs.Status() {
		before[info.ID] = true
	}
	metrics := newTurnMetrics()
	metrics.response.DisplayName = display
	h.a.HandleRoute(context.Background(), h.p.newTurnConductor(metrics), metrics, RouteDecision{Kind: RouteSpawn, Transcript: task, Source: "test"})
	for _, info := range h.a.jobs.Status() {
		if !before[info.ID] {
			if info.State == job.StateRunning {
				h.awaitJobTools(info.ID, len(h.worker.calls(task)))
			}
			return info.ID
		}
	}
	h.t.Fatalf("no job started for %q", task)
	return ""
}

func (h *personaControlHarness) spawn(task string) string { return h.spawnAs("robin", task) }

func (h *personaControlHarness) awaitJobTools(id string, n int) {
	h.t.Helper()
	for h.toolsSeen[id] < n {
		select {
		case got := <-h.jobTool:
			h.toolsSeen[got]++
		case <-time.After(3 * time.Second):
			h.t.Fatalf("job %s reported %d of %d tools", id, h.toolsSeen[id], n)
		}
	}
}

func (h *personaControlHarness) state(id string) job.State { return h.a.jobState(id) }

// controlTurnResult is what one turn did.
type controlTurnResult struct {
	err      error
	chats    int
	tools    [][]string
	spoken   []string
	notices  []string
	outcomes []controlOutcome
	added    []brain.Turn
	jobs     int
	live     int
}

// turn runs one spoken or typed turn whose model replies follow script.
func (h *personaControlHarness) turn(voice bool, utterance string, script func(string) []api.ChatResponse) controlTurnResult {
	h.t.Helper()
	return h.turnCtx(context.Background(), voice, utterance, script)
}

func (h *personaControlHarness) turnCtx(ctx context.Context, voice bool, utterance string, script func(string) []api.ChatResponse) controlTurnResult {
	h.t.Helper()
	h.mu.Lock()
	h.chats, h.tools, h.script, h.notices, h.outcomes, h.live0 = 0, nil, script, nil, nil, 0
	h.mu.Unlock()
	spoke, history, jobs := len(h.tts.Calls()), len(h.live.History()), len(h.a.jobs.Status())
	var err error
	if voice {
		h.p.STT, h.p.VAD, h.p.Capture = &fakeSTT{text: utterance}, &fakeVAD{}, newFakeCapture()
		_, err = h.p.RunTurn(ctx)
	} else {
		err = h.p.RunTurnTextMode(ctx, utterance)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return controlTurnResult{
		err: err, chats: h.chats, tools: h.tools, spoken: h.tts.Calls()[spoke:],
		notices: slices.Clone(h.notices), outcomes: slices.Clone(h.outcomes),
		added: h.live.History()[history:], jobs: len(h.a.jobs.Status()) - jobs, live: h.live0,
	}
}

// replies scripts a turn: the person's line gets round, any later round of
// the same turn gets the next ones.
func replies(rounds ...[]api.ChatResponse) func(string) []api.ChatResponse {
	var mu sync.Mutex
	n := 0
	return func(string) []api.ChatResponse {
		mu.Lock()
		defer mu.Unlock()
		n++
		if n > len(rounds) {
			return nil
		}
		return rounds[n-1]
	}
}

func controlCall(action, id string, extra ...string) api.ToolCall {
	args := api.NewToolCallFunctionArguments()
	args.Set("action", action)
	args.Set("job_id", id)
	for i := 0; i+1 < len(extra); i += 2 {
		args.Set(extra[i], extra[i+1])
	}
	return api.ToolCall{ID: "control-1", Function: api.ToolCallFunction{Name: "job_control", Arguments: args}}
}

func toolCall(name string, kv ...string) api.ToolCall {
	args := api.NewToolCallFunctionArguments()
	for i := 0; i+1 < len(kv); i += 2 {
		args.Set(kv[i], kv[i+1])
	}
	return api.ToolCall{ID: name + "-1", Function: api.ToolCallFunction{Name: name, Arguments: args}}
}

func batch(c ...api.ToolCall) []api.ChatResponse {
	return []api.ChatResponse{{Message: api.Message{Role: "assistant", ToolCalls: c}, Done: true, DoneReason: "stop"}}
}

// settled asserts the turn's whole effect: one model request (or want), no
// live tool, no new job, at most one host line spoken, and that line and the
// persona's words saved as the one reply.
func (r controlTurnResult) settled(t *testing.T, requests int, preamble, line string) controlOutcome {
	t.Helper()
	if r.err != nil {
		t.Fatalf("turn: %v", r.err)
	}
	if r.chats != requests {
		t.Fatalf("model requests = %d, want %d", r.chats, requests)
	}
	if r.jobs != 0 {
		t.Fatalf("%d jobs started", r.jobs)
	}
	if len(r.outcomes) != 1 {
		t.Fatalf("outcomes = %+v, want one", r.outcomes)
	}
	var want []string
	if preamble != "" {
		want = append(want, preamble)
	}
	if line != "" {
		want = append(want, line)
	}
	if strings.Join(r.spoken, " | ") != strings.Join(want, " | ") {
		t.Fatalf("spoken = %q, want %q", r.spoken, want)
	}
	var replies []string
	for _, turn := range r.added {
		if turn.Role == "assistant" && len(turn.ToolCalls) == 0 {
			replies = append(replies, turn.Content)
		}
	}
	saved := strings.TrimSpace(strings.Join(want, " "))
	if saved == "" && len(replies) != 0 || saved != "" && (len(replies) != 1 || replies[0] != saved) {
		t.Fatalf("saved replies = %q, want %q", replies, saved)
	}
	return r.outcomes[0]
}

func (r controlTurnResult) ranWork(t *testing.T, action WorkAction) *WorkReceipt {
	t.Helper()
	o := r.outcomes[0]
	if o.Receipt == nil || o.Refusal != "" || o.Receipt.Actor.Kind != WorkActorPersona || o.Receipt.Request.Action != action {
		t.Fatalf("outcome = %+v, want one %s receipt", o, action)
	}
	return o.Receipt
}

func (r controlTurnResult) refused(t *testing.T, reason string) {
	t.Helper()
	if o := r.outcomes[0]; o.Receipt != nil || o.Refusal != reason {
		t.Fatalf("outcome = %+v, want refusal %q", o, reason)
	}
}

func TestPersonaCancelsItsOwnRunningJob(t *testing.T) {
	for _, voice := range []bool{true, false} {
		t.Run(map[bool]string{true: "voice", false: "text"}[voice], func(t *testing.T) {
			h := newPersonaControlHarness(t)
			id := h.spawn("Summarize the quarterly notes")
			r := h.turn(voice, "Cancel the summary.", replies(batch(controlCall("cancel", id))))
			r.settled(t, 1, "", "Stopped.")
			r.ranWork(t, WorkCancel)
			if h.state(id) != job.StateAborted {
				t.Fatalf("job state = %s", h.state(id))
			}
			if !slices.Contains(r.tools[0], "job_control") {
				t.Fatalf("tools offered = %v", r.tools[0])
			}
		})
	}
}

func TestPersonaReportsItsOwnJob(t *testing.T) {
	h := newPersonaControlHarness(t)
	id := h.spawn("Summarize the quarterly notes")
	r := h.turn(true, "That's enough, what have you got so far?", replies(batch(controlCall("report", id))))
	r.settled(t, 1, "", "I asked it to report, and it's writing up what it has; so far: listed 1 folder.")
	r.ranWork(t, WorkReport)
	if checkpoint, ok := h.a.steerFor(id).control.Reported(); !ok || checkpoint != "listed 1 folder (1 unknown)" {
		t.Fatalf("reported = %q %t", checkpoint, ok)
	}
	if h.state(id) != job.StateRunning {
		t.Fatalf("a reported job is %s", h.state(id))
	}
}

func TestPersonaCorrectsItsOwnJob(t *testing.T) {
	h := newPersonaControlHarness(t)
	running := h.spawn("Summarize the quarterly notes")
	queued := h.spawn("Download the release archive")
	if h.state(queued) != job.StateQueued {
		t.Fatalf("second job is %s", h.state(queued))
	}
	r := h.turn(true, "Use notes-final.txt instead.", replies(batch(controlCall("steer", running, "instruction", "Use notes-final.txt instead of notes.txt."))))
	r.settled(t, 1, "", steerSentLine)
	r.ranWork(t, WorkSteer)
	if got := h.a.steerFor(running).control.Corrections(); len(got) != 1 || got[0].Text != "Use notes-final.txt instead of notes.txt." {
		t.Fatalf("corrections = %+v", got)
	}

	r = h.turn(true, "For the download, grab the arm64 archive.", replies(batch(controlCall("steer", queued, "instruction", "Get the arm64 archive."))))
	r.settled(t, 1, "", steerBriefedLine)
	if got := h.a.steerFor(queued).control.Corrections(); len(got) != 1 || !got[0].Brief {
		t.Fatalf("queued corrections = %+v", got)
	}
	if got := h.a.steerFor(running).control.Corrections(); len(got) != 1 {
		t.Fatalf("the running job was corrected again: %+v", got)
	}
}

func TestPersonaControlsAQueuedJob(t *testing.T) {
	h := newPersonaControlHarness(t)
	running := h.spawn("Summarize the quarterly notes")
	queued := h.spawn("Download the release archive")
	r := h.turn(true, "What has the download got?", replies(batch(controlCall("report", queued))))
	r.settled(t, 1, "", reportQueuedLine)
	r = h.turn(true, "Cancel the download.", replies(batch(controlCall("cancel", queued))))
	r.settled(t, 1, "", "Dropped 1 queued job.")
	if h.state(queued) != job.StateAborted || h.state(running) != job.StateRunning {
		t.Fatalf("states: running=%s queued=%s", h.state(running), h.state(queued))
	}
}

// A slow worker gets the same wording, after the same short wait, as the
// spoken cancel phrase.
func TestPersonaCancelOfASlowWorker(t *testing.T) {
	h := newPersonaControlHarness(t)
	h.worker.stubborn = true
	h.a.ReplaceWorker(h.worker, h.cfg.Clone(), nil)
	h.a.cancelExitWait = 20 * time.Millisecond
	id := h.spawn("Summarize the quarterly notes")
	r := h.turn(true, "Kill the summary job.", replies(batch(controlCall("cancel", id))))
	r.settled(t, 1, "", "Cancelled; the worker is still stopping.")
	if receipt := r.ranWork(t, WorkCancel); receipt.Jobs[0].Outcome != ControlStopping {
		t.Fatalf("receipt = %+v", receipt)
	}
}

// Only a job the turn's prompt named, that this persona started, can change.
func TestPersonaControlRefusesAJobItMayNotTouch(t *testing.T) {
	h := newPersonaControlHarness(t, func(c *config.Config) {})
	own := h.spawn("Summarize the quarterly notes")
	other := h.spawnAs("sage", "Render the demo video")

	r := h.turn(true, "Cancel the video render.", replies(batch(controlCall("cancel", other))))
	r.settled(t, 1, "", notOwnLine)
	r.refused(t, refusedNotOwn)
	if h.state(other) != job.StateQueued || h.state(own) != job.StateRunning {
		t.Fatalf("states: own=%s other=%s", h.state(own), h.state(other))
	}

	for name, id := range map[string]string{"unknown": "job-77", "near miss": " " + own, "case": strings.ToUpper(own)} {
		t.Run(name, func(t *testing.T) {
			r := h.turn(true, "Cancel the summary.", replies(batch(controlCall("cancel", id))))
			r.settled(t, 1, "", "Which job do you mean? Mine is Summarize the quarterly notes.")
			r.refused(t, refusedUnlisted)
			if h.state(own) != job.StateRunning {
				t.Fatalf("own job is %s", h.state(own))
			}
		})
	}
}

func TestPersonaControlAsksWhichWhenItCannotTell(t *testing.T) {
	h := newPersonaControlHarness(t)
	first := h.spawn("Download the release archive")
	second := h.spawn("Download the release archive")
	which := "Which job do you mean: Download the release archive, the running one"
	for name, round := range map[string][]api.ChatResponse{
		"missing id":     batch(toolCall("job_control", "action", "cancel")),
		"two different":  batch(controlCall("cancel", first), controlCall("cancel", second)),
		"cancel, report": batch(controlCall("cancel", first), controlCall("report", first)),
	} {
		t.Run(name, func(t *testing.T) {
			r := h.turn(true, "Cancel the download.", replies(round))
			if len(r.spoken) != 1 || !strings.HasPrefix(r.spoken[0], which) || !strings.HasSuffix(r.spoken[0], "; or Download the release archive, the queued one?") {
				t.Fatalf("spoken = %q", r.spoken)
			}
			r.settled(t, 1, "", r.spoken[0])
			if h.state(first) != job.StateRunning || h.state(second) != job.StateQueued {
				t.Fatalf("states: %s %s", h.state(first), h.state(second))
			}
		})
	}
	// Identical calls are one.
	r := h.turn(true, "Cancel the queued download.", replies(batch(controlCall("cancel", second), controlCall("cancel", second))))
	r.settled(t, 1, "", "Dropped 1 queued job.")
	if h.state(second) != job.StateAborted || h.state(first) != job.StateRunning {
		t.Fatalf("states: %s %s", h.state(first), h.state(second))
	}
}

func TestPersonaControlSaysWhatItCouldNotDo(t *testing.T) {
	h := newPersonaControlHarness(t)
	id := h.spawn("Summarize the quarterly notes")
	truncated := batch(controlCall("cancel", id))
	truncated[0].DoneReason = "length"
	for name, round := range map[string][]api.ChatResponse{
		"malformed":                 batch(controlCall("delete", id)),
		"steer without instruction": batch(controlCall("steer", id)),
		"truncated":                 truncated,
	} {
		t.Run(name, func(t *testing.T) {
			r := h.turn(true, "Cancel the summary.", replies(round))
			r.settled(t, 1, "", controlFailedLine)
			if h.state(id) != job.StateRunning {
				t.Fatalf("job is %s", h.state(id))
			}
		})
	}
}

func TestPersonaControlWithNoJobs(t *testing.T) {
	h := newPersonaControlHarness(t)
	r := h.turn(true, "Cancel the summary.", replies(batch(controlCall("cancel", "job-1"))))
	r.settled(t, 1, "", nothingRunningLine)
	r.refused(t, refusedNoJobs)
}

// A job past the eighth is counted in the prompt, not named, so it cannot be
// controlled by its id.
func TestPersonaControlCannotReachAJobThePromptDidNotName(t *testing.T) {
	h := newPersonaControlHarness(t)
	var ids []string
	for range 9 {
		ids = append(ids, h.spawn("Audit the config"))
	}
	r := h.turn(true, "Cancel the last audit.", replies(batch(controlCall("cancel", ids[8]))))
	if len(r.outcomes) != 1 || r.outcomes[0].Refusal != refusedUnlisted || h.state(ids[8]) != job.StateQueued {
		t.Fatalf("outcome = %+v, state = %s", r.outcomes, h.state(ids[8]))
	}
	r = h.turn(true, "Cancel the first audit.", replies(batch(controlCall("cancel", ids[0]))))
	r.settled(t, 1, "", "Stopped.")
}

func TestPersonaControlOfAJobThatEndedSinceTheList(t *testing.T) {
	h := newPersonaControlHarness(t)
	id := h.spawn("Summarize the quarterly notes")
	h.before = func() {
		close(h.release)
		select {
		case <-h.finished:
		case <-time.After(3 * time.Second):
			t.Error("the job never finished")
		}
	}
	r := h.turn(true, "Cancel the summary.", replies(batch(controlCall("cancel", id))))
	// The job's own result is delivered as it would be; the control adds one line.
	if !slices.Contains(r.spoken, alreadyDoneLine) || len(r.outcomes) != 1 || r.chats != 1 {
		t.Fatalf("spoken = %q", r.spoken)
	}
	if o := r.outcomes[0]; o.Receipt == nil || o.Receipt.Jobs[0].Outcome != ControlFinished {
		t.Fatalf("outcome = %+v", o)
	}
}

// The turn's authority ends with the turn's conversation and persona.
func TestPersonaControlAfterTheTurnStoppedBeingLive(t *testing.T) {
	for name, change := range map[string]func(h *personaControlHarness){
		"conversation cleared": func(h *personaControlHarness) {
			scope := h.p.ConversationScope()
			scope.ConversationEpoch = newWorkID()
			h.p.adoptConversationScope(scope)
		},
		"persona switched": func(h *personaControlHarness) {
			cfg := h.cfg.Clone()
			cfg.ActivePersona = "sage"
			h.a.ReplaceWorker(h.worker, cfg, nil)
		},
	} {
		t.Run(name, func(t *testing.T) {
			h := newPersonaControlHarness(t)
			id := h.spawn("Summarize the quarterly notes")
			h.before = func() { change(h) }
			r := h.turn(true, "Cancel the summary.", replies(batch(controlCall("cancel", id))))
			r.settled(t, 1, "", "")
			r.refused(t, refusedNotLive)
			if !slices.Contains(r.notices, notLiveNotice) || h.state(id) != job.StateRunning {
				t.Fatalf("notices = %q, job = %s", r.notices, h.state(id))
			}
		})
	}
}

func TestPersonaControlInATurnStoppedBeforeTheCallLands(t *testing.T) {
	h := newPersonaControlHarness(t)
	id := h.spawn("Summarize the quarterly notes")
	ctx, cancel := context.WithCancel(context.Background())
	h.before = cancel
	r := h.turnCtx(ctx, false, "Cancel the summary.", replies(batch(controlCall("cancel", id))))
	if len(r.outcomes) != 0 || len(r.spoken) != 0 || h.state(id) != job.StateRunning {
		t.Fatalf("stopped turn: outcomes=%+v spoken=%q job=%s", r.outcomes, r.spoken, h.state(id))
	}
}

func TestPersonaControlInATurnMovedToTheBackground(t *testing.T) {
	h := newPersonaControlHarness(t)
	h.p.NamesOtherPersona = func(string) bool { return true }
	id := h.spawn("Summarize the quarterly notes")
	moved := make(chan bool, 1)
	h.before = func() { moved <- h.p.MoveTurnToBackground("Sage, are you there?") }
	r := h.turn(true, "Cancel the summary.", replies(batch(controlCall("cancel", id))))
	if !<-moved {
		t.Fatal("the turn did not move")
	}
	if len(r.outcomes) != 0 || h.state(id) != job.StateRunning {
		t.Fatalf("moved turn: outcomes=%+v job=%s", r.outcomes, h.state(id))
	}
}

func TestBareStopNeverControlsAJob(t *testing.T) {
	for _, utterance := range []string{"Stop.", "stop", "STOP!", "  Stop?  "} {
		t.Run(utterance, func(t *testing.T) {
			h := newPersonaControlHarness(t)
			id := h.spawn("Download the release archive")
			r := h.turn(true, utterance, replies(batch(controlCall("cancel", id))))
			r.settled(t, 1, "", "")
			r.refused(t, refusedBareStop)
			if !slices.Contains(r.notices, bareStopNotice) || h.state(id) != job.StateRunning {
				t.Fatalf("notices = %q, job = %s", r.notices, h.state(id))
			}
		})
	}
	h := newPersonaControlHarness(t)
	id := h.spawn("Download the release archive")
	r := h.turn(true, "Stop the download.", replies(batch(controlCall("cancel", id))))
	r.settled(t, 1, "", "Stopped.")
	if h.state(id) != job.StateAborted {
		t.Fatalf("job is %s", h.state(id))
	}
}

// The exact cancel phrases keep their own path and never reach the model.
func TestCancelPhrasesStillSkipTheModel(t *testing.T) {
	h := newPersonaControlHarness(t)
	id := h.spawn("Download the release archive")
	r := h.turn(true, "Cancel that.", nil)
	if r.err != nil || r.chats != 0 || len(r.outcomes) != 0 || h.state(id) != job.StateAborted || strings.Join(r.spoken, "|") != "Stopped." {
		t.Fatalf("phrase cancel: %+v", r)
	}
}

// What the persona said before the call is spoken once and kept, the host's
// line follows, and nothing after the call exists.
func TestPersonaWordsBeforeTheControlAreKept(t *testing.T) {
	for _, voice := range []bool{true, false} {
		t.Run(map[bool]string{true: "voice", false: "text"}[voice], func(t *testing.T) {
			h := newPersonaControlHarness(t)
			id := h.spawn("Summarize the quarterly notes")
			round := []api.ChatResponse{
				{Message: api.Message{Role: "assistant", Content: "Sure thing."}},
				{Message: api.Message{Role: "assistant", ToolCalls: []api.ToolCall{controlCall("cancel", id)}}},
				{Message: api.Message{Role: "assistant", Content: " Done, it is cancelled."}},
				{Message: api.Message{Role: "assistant"}, Done: true, DoneReason: "stop"},
			}
			r := h.turn(voice, "Cancel the summary.", replies(round))
			r.settled(t, 1, "Sure thing.", "Stopped.")
		})
	}
}

// Nothing beside the control runs and no job starts, after a look around or
// beside a hand-off.
func TestPersonaControlBatchStartsNothing(t *testing.T) {
	for name, rounds := range map[string][][]api.ChatResponse{
		"beside background work":  {batch(toolCall("start_background_work"), controlCall("cancel", "job-1"))},
		"beside a search":         {batch(controlCall("cancel", "job-1"), toolCall("web_search", "query", "weather"))},
		"after listing the files": {batch(toolCall("list_files", "path", ".")), batch(controlCall("cancel", "job-1"), toolCall("start_background_work"))},
	} {
		t.Run(name, func(t *testing.T) {
			h := newPersonaControlHarness(t)
			id := h.spawn("Summarize the quarterly notes")
			if id != "job-1" {
				t.Fatalf("job id %q", id)
			}
			r := h.turn(true, "Cancel the summary.", replies(rounds...))
			r.settled(t, len(rounds), "", "Stopped.")
			if want := len(rounds) - 1; r.live != want {
				t.Fatalf("live tools run = %d, want %d", r.live, want)
			}
		})
	}
}

// A stray call where the tool is not offered changes nothing and starts
// nothing.
func TestPersonaControlWhereTheToolIsNotOffered(t *testing.T) {
	t.Run("route only", func(t *testing.T) {
		h := newPersonaControlHarness(t, func(c *config.Config) { c.VoiceRouterTools = "route_only" })
		id := h.spawn("Summarize the quarterly notes")
		r := h.turn(true, "Cancel the summary.", replies(batch(controlCall("cancel", id))))
		r.settled(t, 1, "", controlFailedLine)
		r.refused(t, string(brain.JobControlNotOffered))
		if slices.Contains(r.tools[0], "job_control") || h.state(id) != job.StateRunning {
			t.Fatalf("tools = %v, job = %s", r.tools[0], h.state(id))
		}
	})
	t.Run("option off", func(t *testing.T) {
		h := newPersonaControlHarness(t, func(c *config.Config) { c.VoiceJobControl = false })
		id := h.spawn("Summarize the quarterly notes")
		r := h.turn(true, "Cancel the summary.", replies(batch(controlCall("cancel", id)), []api.ChatResponse{{Message: api.Message{Role: "assistant", Content: "I can't do that."}, Done: true, DoneReason: "stop"}}))
		if r.err != nil || r.chats != 2 || len(r.outcomes) != 0 || r.jobs != 0 || h.state(id) != job.StateRunning || slices.Contains(r.tools[0], "job_control") {
			t.Fatalf("option off: %+v job=%s", r, h.state(id))
		}
	})
}

// A job the turn's list did not name is refused as unknown, before ownership
// is looked at, so the refusal says nothing about whose it is.
func TestPersonaControlOfAJobTheListLeftOut(t *testing.T) {
	h := newPersonaControlHarness(t)
	own := h.spawn("Summarize the quarterly notes")
	hidden := h.spawnAs("sage", "Read the private tax notes")
	metrics := newTurnMetrics()
	token := h.p.controlTurns.Add(1)
	h.p.liveControl.Store(token)
	metrics.controlTurn = &controlTurn{token: token, persona: "robin", scope: h.p.ConversationScope(), listed: []string{own}, request: "Cancel the tax job.", authority: true}
	metrics.jobControl = &brain.JobControl{Request: &brain.JobControlRequest{Action: brain.JobControlCancel, JobID: hidden}}
	out := h.a.decideJobControl(context.Background(), metrics, metrics.jobControl)
	if out.Refusal != refusedUnlisted || out.Receipt != nil || strings.Contains(out.Line, "tax") || out.Line != "Which job do you mean? Mine is Summarize the quarterly notes." {
		t.Fatalf("outcome = %+v", out)
	}
	if h.state(hidden) != job.StateQueued {
		t.Fatalf("hidden job is %s", h.state(hidden))
	}
	// The grant is spent: the same turn cannot try again.
	metrics.jobControl.Request.JobID = own
	if out := h.a.decideJobControl(context.Background(), metrics, metrics.jobControl); out.Refusal != refusedNotLive || h.state(own) != job.StateRunning {
		t.Fatalf("second control in one turn: %+v", out)
	}
}

// ControlWork gives the persona actor only its own jobs in this conversation,
// never all of them, and never a job a clear left behind.
func TestControlWorkPersonaActorReachesOnlyItsOwnJobs(t *testing.T) {
	h := newPersonaControlHarness(t)
	own := h.spawn("Summarize the quarterly notes")
	other := h.spawnAs("sage", "Render the demo video")
	robin := WorkActor{Kind: WorkActorPersona, Persona: "robin"}
	if r := h.a.ControlWork(robin, WorkRequest{Action: WorkCancel, All: true}); r.Rejected == "" {
		t.Fatalf("cancel all: %+v", r)
	}
	if r := h.a.ControlWork(robin, WorkRequest{Action: WorkCancel, JobID: other}); len(r.Jobs) != 1 || r.Jobs[0].Outcome != ControlUnknown || h.state(other) != job.StateQueued {
		t.Fatalf("another persona's job: %+v", r)
	}
	scope := h.p.ConversationScope()
	scope.ConversationEpoch = newWorkID()
	h.p.adoptConversationScope(scope)
	if r := h.a.ControlWork(robin, WorkRequest{Action: WorkCancel, JobID: own}); len(r.Jobs) != 1 || r.Jobs[0].Outcome != ControlUnknown || h.state(own) != job.StateRunning {
		t.Fatalf("a job a clear left behind: %+v", r)
	}
	if r := h.a.ControlWork(WorkActor{Kind: WorkActorCommand}, WorkRequest{Action: WorkCancel, JobID: own}); r.Err() != nil || h.state(own) != job.StateAborted {
		t.Fatalf("the command still reaches it: %+v", r)
	}
}
