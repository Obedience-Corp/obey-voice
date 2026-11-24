package brain

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ollama/ollama/api"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/discussion"
)

// liveControl is what the host gives a live direct turn answering a person.
var liveControl = StreamOptions{ToolsEnabled: true, RouterToolsPolicy: RouterToolsDirect, BackgroundWorker: true, JobControl: true}

func control(action, id string, extra ...any) api.ToolCall {
	args := map[string]any{"action": action, "job_id": id}
	for i := 0; i+1 < len(extra); i += 2 {
		args[extra[i].(string)] = extra[i+1]
	}
	return api.ToolCall{ID: "control-1", Function: api.ToolCallFunction{Name: jobControlToolName, Arguments: mustArgs(args)}}
}

func named(name string, args map[string]any) api.ToolCall {
	return api.ToolCall{ID: name + "-1", Function: api.ToolCallFunction{Name: name, Arguments: mustArgs(args)}}
}

// controlModel is a fake Ollama that answers request n with rounds[n-1] and
// fails the test on any request past them.
func controlModel(t *testing.T, rounds ...[]api.ChatResponse) (*OllamaBrain, func() []api.ChatRequest) {
	t.Helper()
	config.SetConfigDirForTest(t, t.TempDir())
	o, sent := toolModel(t, true, func(w http.ResponseWriter, n int, req api.ChatRequest) {
		if n > len(rounds) {
			t.Errorf("request %d was not scripted", n)
			http.Error(w, "unscripted", http.StatusInternalServerError)
			return
		}
		enc := json.NewEncoder(w)
		for _, line := range rounds[n-1] {
			_ = enc.Encode(line)
		}
	})
	o.cfg.VoiceJobControl = true
	return o, sent
}

// calls is one complete round that returns these tool calls.
func calls(c ...api.ToolCall) []api.ChatResponse { return []api.ChatResponse{toolResponse(c)} }

func says(text string) []api.ChatResponse { return []api.ChatResponse{answerResponse(text)} }

type controlRun struct {
	routedTurn
	sent []api.ChatRequest
	o    *OllamaBrain
}

func runControl(t *testing.T, input string, opts StreamOptions, rounds ...[]api.ChatResponse) controlRun {
	t.Helper()
	o, sent := controlModel(t, rounds...)
	return controlRun{routedTurn: runRoutedTurn(t, o, input, opts), sent: sent(), o: o}
}

// ended asserts the turn ended at the control batch: one job_control result,
// no tool executed, no further request.
func (r controlRun) ended(t *testing.T, requests int) *JobControl {
	t.Helper()
	if r.result.Route != JobControlRoute || r.result.JobControl == nil {
		t.Fatalf("turn did not end at the control: %+v", r.result)
	}
	if len(r.sent) != requests {
		t.Fatalf("requests = %d, want %d", len(r.sent), requests)
	}
	for _, tool := range r.tools {
		if tool.Name == jobControlToolName || tool.Name == backgroundWorkTool {
			t.Fatalf("%s ran as a tool", tool.Name)
		}
	}
	for _, m := range r.o.history {
		for _, call := range m.ToolCalls {
			if call.Function.Name == jobControlToolName {
				t.Fatalf("the control batch was written to history: %+v", m)
			}
		}
	}
	return r.result.JobControl
}

func TestJobControlIsOfferedOnlyOnLiveDirectTurnsAnsweringAPerson(t *testing.T) {
	o, _ := controlModel(t)
	off, _ := controlModel(t)
	off.cfg.VoiceJobControl = false
	worker, _ := controlModel(t)
	worker.cfg.IsWorker = true
	with := func(f func(*StreamOptions)) StreamOptions {
		opts := liveControl
		f(&opts)
		return opts
	}
	for name, tc := range map[string]struct {
		brain *OllamaBrain
		opts  StreamOptions
		want  bool
	}{
		"live direct":         {o, liveControl, true},
		"option off":          {off, liveControl, false},
		"host cannot control": {o, with(func(s *StreamOptions) { s.JobControl = false }), false},
		"worker":              {worker, liveControl, false},
		"worker budget":       {o, with(func(s *StreamOptions) { s.WorkerBudget = &WorkerBudget{} }), false},
		"worker control":      {o, with(func(s *StreamOptions) { s.Control = NewWorkerControl(nil) }), false},
		"background":          {o, with(func(s *StreamOptions) { s.Background = true }), false},
		"continuation":        {o, with(func(s *StreamOptions) { s.AgentContinuation = true }), false},
		"discussion":          {o, with(func(s *StreamOptions) { s.Discussion = &discussion.Directive{} }), false},
		"transient":           {o, with(func(s *StreamOptions) { s.TransientHistory = true }), false},
		"summary":             {o, with(func(s *StreamOptions) { s.OmitTurnInstruction = true }), false},
		"retry":               {o, with(func(s *StreamOptions) { s.RetryInput = true }), false},
		"tools off":           {o, with(func(s *StreamOptions) { s.ToolsEnabled = false }), false},
		"route only":          {o, with(func(s *StreamOptions) { s.RouterToolsPolicy = RouterToolsRouteOnly }), false},
	} {
		if got := hasTool(tc.brain.turnToolSession(tc.opts).tools(), jobControlToolName); got != tc.want {
			t.Errorf("%s: job_control offered = %t", name, got)
		}
	}
}

// The tool sits between view_image and start_background_work, with or
// without jobs, so the request prefix does not move.
func TestJobControlHasAFixedPlaceInTheLiveRequest(t *testing.T) {
	var lists []string
	for _, facts := range [][]WorkerFact{nil, goldenJobs()} {
		opts := liveControl
		opts.WorkerFacts = facts
		r := runControl(t, "How is it going?", opts, says("Fine."))
		var names []string
		for _, tool := range r.sent[0].Tools {
			names = append(names, tool.Function.Name)
		}
		lists = append(lists, strings.Join(names, " "))
	}
	want := "list_files read_file write_file run_command web_search fetch_url view_image job_control start_background_work"
	if lists[0] != want || lists[1] != want {
		t.Fatalf("tool lists = %q, want %q both times", lists, want)
	}
}

// With the option off, what an ordinary turn sends is what it sent before
// the option existed, byte for byte, the warm-up included.
func TestJobControlOffSendsTheRequestsPinnedBeforeIt(t *testing.T) {
	got := liveDirectRequests(t, func(_ *config.Config, opts *StreamOptions) { opts.JobControl = true })
	want, err := os.ReadFile(filepath.Join("testdata", "live_direct_requests.golden"))
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Fatalf("an ordinary turn with job control off sends something else\n got: %s\nwant: %s", got, want)
	}
}

// With it on, the tool is the only difference, in the live request and in
// the warm-up alike.
func TestJobControlOnAddsOnlyTheTool(t *testing.T) {
	got := liveDirectRequests(t, func(cfg *config.Config, opts *StreamOptions) {
		cfg.VoiceJobControl = true
		opts.JobControl = true
	})
	tool, err := json.Marshal(jobControlTool())
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(got, string(tool)); n != 3 {
		t.Fatalf("job_control appears %d times, want in both live requests and the warm-up", n)
	}
	want, err := os.ReadFile(filepath.Join("testdata", "live_direct_requests.golden"))
	if err != nil {
		t.Fatal(err)
	}
	if stripped := strings.ReplaceAll(got, ","+string(tool), ""); stripped != string(want) {
		t.Fatalf("job control on changed more than the tool list\n got: %s\nwant: %s", stripped, want)
	}
}

func TestJobControlDecodesEachAction(t *testing.T) {
	for _, tc := range []struct {
		call api.ToolCall
		want JobControlRequest
	}{
		{control("cancel", "job-1"), JobControlRequest{Action: JobControlCancel, JobID: "job-1"}},
		{control("report", "job-1"), JobControlRequest{Action: JobControlReport, JobID: "job-1"}},
		{control("steer", "job-1", "instruction", "Use notes-final.txt."), JobControlRequest{Action: JobControlSteer, JobID: "job-1", Instruction: "Use notes-final.txt."}},
		{control("cancel", "job-9"), JobControlRequest{Action: JobControlCancel, JobID: "job-9"}},
		{control("cancel", "job-1", "instruction", ""), JobControlRequest{Action: JobControlCancel, JobID: "job-1"}},
	} {
		t.Run(string(tc.want.Action)+" "+tc.want.JobID, func(t *testing.T) {
			r := runControl(t, "Do it.", liveControl, calls(tc.call))
			got := r.ended(t, 1)
			if got.Refusal != "" || got.Request == nil || *got.Request != tc.want {
				t.Fatalf("control = %+v, want %+v", got, tc.want)
			}
			if h := r.o.History(); len(h) != 1 || h[0].Role != "user" {
				t.Fatalf("history = %+v, want only the user's line", h)
			}
		})
	}
}

func TestJobControlRefusesWhatItCannotDecode(t *testing.T) {
	truncated := toolResponse([]api.ToolCall{control("cancel", "job-1")})
	truncated.DoneReason = "length"
	unfinished := toolResponse([]api.ToolCall{control("cancel", "job-1")})
	unfinished.Done = false
	for name, tc := range map[string]struct {
		round []api.ChatResponse
		want  JobControlRefusal
	}{
		"missing id":                 {calls(named(jobControlToolName, map[string]any{"action": "cancel"})), JobControlNoJob},
		"empty id":                   {calls(control("cancel", "  ")), JobControlNoJob},
		"unknown action":             {calls(control("delete", "job-1")), JobControlMalformed},
		"missing action":             {calls(named(jobControlToolName, map[string]any{"job_id": "job-1"})), JobControlMalformed},
		"numeric id":                 {calls(named(jobControlToolName, map[string]any{"action": "cancel", "job_id": 1})), JobControlMalformed},
		"extra field":                {calls(control("cancel", "job-1", "all", true)), JobControlMalformed},
		"instruction not text":       {calls(control("steer", "job-1", "instruction", 3)), JobControlMalformed},
		"steer without instruction":  {calls(control("steer", "job-1")), JobControlMalformed},
		"steer with blank":           {calls(control("steer", "job-1", "instruction", " ")), JobControlMalformed},
		"two different controls":     {calls(control("cancel", "job-1"), control("cancel", "job-2")), JobControlAmbiguous},
		"cancel and report":          {calls(control("cancel", "job-1"), control("report", "job-1")), JobControlAmbiguous},
		"truncated":                  {[]api.ChatResponse{truncated}, JobControlIncomplete},
		"ended without finishing":    {[]api.ChatResponse{unfinished}, JobControlIncomplete},
		"malformed beside a control": {calls(control("cancel", "job-1"), named(jobControlToolName, nil)), JobControlAmbiguous},
	} {
		t.Run(name, func(t *testing.T) {
			r := runControl(t, "Do it.", liveControl, tc.round)
			if got := r.ended(t, 1); got.Request != nil || got.Refusal != tc.want {
				t.Fatalf("control = %+v, want refusal %q", got, tc.want)
			}
		})
	}
}

func TestJobControlCountsIdenticalCallsOnce(t *testing.T) {
	r := runControl(t, "Cancel the summary.", liveControl, calls(control("cancel", "job-1"), control("cancel", "job-1"), control("cancel", "job-1")))
	if got := r.ended(t, 1); got.Request == nil || got.Request.JobID != "job-1" {
		t.Fatalf("control = %+v", got)
	}
}

// A failed response is a refusal: nothing in it ran and nothing is retried.
func TestJobControlInAFailedResponseIsRefused(t *testing.T) {
	config.SetConfigDirForTest(t, t.TempDir())
	o, sent := toolModel(t, true, func(w http.ResponseWriter, n int, req api.ChatRequest) {
		_ = json.NewEncoder(w).Encode(api.ChatResponse{Message: api.Message{Role: "assistant", ToolCalls: []api.ToolCall{control("cancel", "job-1")}}})
		_, _ = w.Write([]byte(`{"error":"runner crashed"}` + "\n"))
	})
	o.cfg.VoiceJobControl = true
	turn := runRoutedTurn(t, o, "Cancel the summary.", liveControl)
	got := turn.result.JobControl
	if turn.result.Route != JobControlRoute || got == nil || got.Refusal != JobControlIncomplete || turn.result.Err == nil || !turn.result.Recovered {
		t.Fatalf("failed response: %+v control=%+v", turn.result, got)
	}
	if len(sent()) != 1 || len(turn.tools) != 0 {
		t.Fatalf("requests=%d tools=%+v", len(sent()), turn.tools)
	}
}

// Nothing beside the control runs, in either order: no hand-off, no search,
// no write, no picture.
func TestJobControlBatchRunsNoSibling(t *testing.T) {
	for name, sibling := range map[string]api.ToolCall{
		"background work": named(backgroundWorkTool, nil),
		"web search":      named("web_search", map[string]any{"query": "weather"}),
		"write":           named("write_file", map[string]any{"path": "result", "content": "written"}),
		"command":         named("run_command", map[string]any{"command": "touch ran"}),
		"view image":      named(viewImageToolName, map[string]any{"path": "shot.png"}),
		"agent cancel":    named("agent_cancel", map[string]any{"ticket": "t-1"}),
	} {
		for order, batch := range map[string][]api.ToolCall{
			"before": {sibling, control("cancel", "job-1")},
			"after":  {control("cancel", "job-1"), sibling},
		} {
			t.Run(name+" "+order, func(t *testing.T) {
				o, sent := controlModel(t, calls(batch...))
				writePNG(t, filepath.Join(o.workDir, "shot.png"))
				turn := runRoutedTurn(t, o, "Cancel the summary.", liveControl)
				r := controlRun{routedTurn: turn, sent: sent(), o: o}
				if got := r.ended(t, 1); got.Request == nil || got.Request.JobID != "job-1" {
					t.Fatalf("control = %+v", got)
				}
				if len(turn.tools) != 0 {
					t.Fatalf("tools ran: %+v", turn.tools)
				}
				for _, file := range []string{"result", "ran"} {
					if _, err := os.Stat(filepath.Join(o.workDir, file)); !os.IsNotExist(err) {
						t.Fatalf("%s exists: a sibling ran", file)
					}
				}
			})
		}
	}
}

// A control after the turn already looked at local files, or opened a
// picture, still ends the turn at the control and keeps what it looked at.
func TestJobControlAfterAnInspectionRound(t *testing.T) {
	for name, first := range map[string]api.ToolCall{
		"list_files":         named("list_files", map[string]any{"path": "."}),
		"read_file":          named("read_file", map[string]any{"path": "notes.txt"}),
		"read_skill":         named("read_skill", map[string]any{"name": "missing"}),
		"view_image":         named(viewImageToolName, map[string]any{"path": "shot.png"}),
		"view_image failed":  named(viewImageToolName, map[string]any{"path": "absent.png"}),
		"write (no handoff)": named("write_file", map[string]any{"path": "out.txt", "content": "x"}),
	} {
		t.Run(name, func(t *testing.T) {
			o, sent := controlModel(t, calls(first), calls(control("report", "job-1"), named(backgroundWorkTool, nil)))
			writePNG(t, filepath.Join(o.workDir, "shot.png"))
			if err := os.WriteFile(filepath.Join(o.workDir, "notes.txt"), []byte("notes"), 0o600); err != nil {
				t.Fatal(err)
			}
			turn := runRoutedTurn(t, o, "What has it got so far?", liveControl)
			r := controlRun{routedTurn: turn, sent: sent(), o: o}
			if got := r.ended(t, 2); got.Request == nil || got.Request.Action != JobControlReport {
				t.Fatalf("control = %+v", got)
			}
			if !hasTool(r.sent[1].Tools, jobControlToolName) {
				t.Fatal("the second round did not offer job_control")
			}
			if len(turn.tools) != 1 || turn.tools[0].Name != first.Function.Name {
				t.Fatalf("tools = %+v, want only the first round's", turn.tools)
			}
			h := o.History()
			if len(h) != 3 || h[0].Role != "user" || h[2].Role != "tool" {
				t.Fatalf("history = %+v, want the user's line and the inspection kept", spokenHistory(h))
			}
		})
	}
}

// The persona's words before the call are sent and kept; words after it in
// the same response are neither.
func TestJobControlEndsWhatTheRoundSays(t *testing.T) {
	round := []api.ChatResponse{
		{Message: api.Message{Role: "assistant", Content: "Okay. "}},
		{Message: api.Message{Role: "assistant", ToolCalls: []api.ToolCall{control("cancel", "job-1")}}},
		{Message: api.Message{Role: "assistant", Content: "Done, I cancelled it."}},
		{Message: api.Message{Role: "assistant"}, Done: true, DoneReason: "stop"},
	}
	r := runControl(t, "Cancel the summary.", liveControl, round)
	got := r.ended(t, 1)
	if got.Request == nil || got.Spoken != "Okay." || strings.TrimSpace(r.answer) != "Okay." {
		t.Fatalf("control = %+v, answer = %q", got, r.answer)
	}
}

// Under the route policies the tool is never offered, and a stray call never
// becomes a spawn.
func TestJobControlUnderRouteOnly(t *testing.T) {
	routeOnly := liveControl
	routeOnly.RouterToolsPolicy = RouterToolsRouteOnly
	r := runControl(t, "Cancel the summary.", routeOnly, calls(control("cancel", "job-1")))
	if got := r.ended(t, 1); got.Refusal != JobControlNotOffered || hasTool(r.sent[0].Tools, jobControlToolName) {
		t.Fatalf("route_only: %+v", got)
	}

	o, sent := controlModel(t, calls(control("cancel", "job-1")), calls(named("route", map[string]any{"kind": "chat"})), says("Which one?"))
	o.cfg.VoiceJobControl = false
	turn := runRoutedTurn(t, o, "Cancel the summary.", routeOnly)
	if turn.result.Route != "" || turn.result.JobControl != nil || turn.result.Err != nil || len(sent()) != 3 {
		t.Fatalf("option off, route_only: %+v requests=%d", turn.result, len(sent()))
	}
	if result := toolResult(t, sent()[1], jobControlToolName); !strings.Contains(result, "error: tool \"job_control\" is not available") {
		t.Fatalf("stray job_control result %q", result)
	}
}

// With the option off a job_control call is any unknown tool.
func TestJobControlOffIsAnUnknownTool(t *testing.T) {
	o, sent := controlModel(t, calls(control("cancel", "job-1")), says("I can't do that."))
	o.cfg.VoiceJobControl = false
	turn := runRoutedTurn(t, o, "Cancel the summary.", liveControl)
	if turn.result.Route != "" || turn.result.JobControl != nil || len(sent()) != 2 || hasTool(sent()[0].Tools, jobControlToolName) {
		t.Fatalf("option off: %+v requests=%d", turn.result, len(sent()))
	}
	if result := toolResult(t, sent()[1], jobControlToolName); !strings.Contains(result, "unknown tool: job_control") {
		t.Fatalf("unknown tool result %q", result)
	}
}

// Turns that answer no person, and recovery rounds, cannot control a job.
func TestJobControlIsRefusedOffTheLiveTurn(t *testing.T) {
	t.Run("background", func(t *testing.T) {
		opts := liveControl
		opts.Background = true
		r := runControl(t, "", opts, calls(control("cancel", "job-1")))
		if got := r.ended(t, 1); got.Refusal != JobControlNotOffered {
			t.Fatalf("background: %+v", got)
		}
	})
	t.Run("tools off", func(t *testing.T) {
		opts := liveControl
		opts.ToolsEnabled = false
		r := runControl(t, "Cancel the summary.", opts, calls(control("cancel", "job-1")), says("I can't."))
		if r.result.JobControl != nil || len(r.sent) != 2 || len(r.sent[1].Tools) != 0 {
			t.Fatalf("tools off: %+v requests=%d", r.result, len(r.sent))
		}
	})
	t.Run("recovery", func(t *testing.T) {
		routeOnly := liveControl
		routeOnly.RouterToolsPolicy = RouterToolsRouteOnly
		r := runControl(t, "Cancel the summary.", routeOnly, calls(named("route", map[string]any{"kind": "chat"})), calls(control("cancel", "job-1")))
		if r.result.JobControl != nil || r.result.Err == nil || len(r.sent) != 2 {
			t.Fatalf("a recovery round controlled a job: %+v requests=%d", r.result, len(r.sent))
		}
	})
}

func TestWorkerRequestsNeverOfferJobControl(t *testing.T) {
	o, sent := controlModel(t, says("Done."))
	if _, err := o.ThinkFull(t.Context(), "Summarize the notes.", liveControl); err != nil {
		t.Fatal(err)
	}
	if hasTool(sent()[0].Tools, jobControlToolName) {
		t.Fatal("a blocking request offered job_control")
	}
}

func TestListedWorkerFactIDsAreTheOnesRendered(t *testing.T) {
	var facts []WorkerFact
	for i := range 10 {
		facts = append(facts, WorkerFact{ID: "job-" + string(rune('a'+i)), Tier: "legacy", State: "running", Task: "task", Own: true})
	}
	facts[1].ID = "bad id"
	listed := ListedWorkerFactIDs(facts)
	prompt := WorkerFactsPrompt(nil, facts)
	if len(listed) != 7 {
		t.Fatalf("listed = %v, want the seven valid jobs of the first eight", listed)
	}
	for _, id := range listed {
		if !strings.Contains(prompt, `job="`+id+`"`) {
			t.Fatalf("%s listed but not rendered", id)
		}
	}
	for _, id := range []string{"job-i", "job-j", "bad id"} {
		if strings.Contains(prompt, `job="`+id+`"`) || bytes.Contains([]byte(strings.Join(listed, " ")), []byte(id)) {
			t.Fatalf("%s rendered or listed", id)
		}
	}
	long := []WorkerFact{}
	for i := range 8 {
		long = append(long, WorkerFact{ID: "job-" + string(rune('a'+i)), Tier: "legacy", State: "running", Task: strings.Repeat("x", 160), Progress: strings.Repeat("y", 200), Starter: strings.Repeat("z", 48)})
	}
	listed, prompt = ListedWorkerFactIDs(long), WorkerFactsPrompt(nil, long)
	if strings.Count(prompt, "\njob=") != len(listed) {
		t.Fatalf("byte ceiling: rendered %d, listed %d", strings.Count(prompt, "\njob="), len(listed))
	}
}
