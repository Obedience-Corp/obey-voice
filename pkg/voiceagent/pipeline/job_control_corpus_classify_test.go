package pipeline

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/ollama/ollama/api"
)

func corpusCaseByID(t *testing.T, id string) corpusCase {
	t.Helper()
	for _, c := range jobControlCorpus() {
		if c.ID == id {
			return c
		}
	}
	t.Fatalf("no corpus case %s", id)
	return corpusCase{}
}

func TestCorpusClassifierCountsChangesNotCalls(t *testing.T) {
	control := []corpusCall{{Request: 1, Name: "job_control", Args: map[string]any{"action": "cancel", "job_id": "job-1"}}}
	for _, tc := range []struct {
		name    string
		c       string
		rec     corpusRecord
		flags   []string
		outcome string
	}{
		{"clear and right", "1-R1", corpusRecord{Calls: control, Decision: "controlled", Changes: []corpusChange{{Job: "summary", Own: true, Change: "cancelled"}}}, []string{"c5_correct", "control_call"}, "correct"},
		{"clear, other job changed", "7-R2", corpusRecord{Calls: control, Changes: []corpusChange{{Job: "download-4.3", Own: true, Change: "cancelled"}}}, []string{"c1_wrong_job", "control_call"}, "wrong_job"},
		{"clear, wrong action", "3-R1", corpusRecord{Calls: control, Changes: []corpusChange{{Job: "summary", Own: true, Change: "cancelled"}}}, []string{"wrong_action", "control_call"}, "wrong_action"},
		{"clear, no change", "1-R1", corpusRecord{Decision: "none"}, nil, "no_change"},
		{"clear, spawned", "1-R1", corpusRecord{Spawned: 1, Calls: []corpusCall{{Name: "start_background_work"}}}, []string{"c4_spawn_for_control", "spawned"}, "spawned"},
		{"steer missing its detail", "5-R1", corpusRecord{Changes: []corpusChange{{Job: "summary", Own: true, Change: "corrected", Detail: "use the other notes"}}}, []string{"instruction_missing_detail"}, "changed_corrected"},
		{"steer with its detail", "5-R1", corpusRecord{Changes: []corpusChange{{Job: "summary", Own: true, Change: "corrected", Detail: "Use notes-final.txt"}}}, []string{"c5_correct"}, "correct"},
		{"bare stop called but refused", "15-R1", corpusRecord{Calls: control, Decision: "refused", Refusal: refusedBareStop}, []string{"control_call"}, "refused_bare_stop"},
		{"related cancel", "16-R1", corpusRecord{Changes: []corpusChange{{Job: "summary", Own: true, Change: "cancelled"}}}, []string{"c2_unrequested_mutation"}, "unrequested_change"},
		{"related report is allowed", "20-R1", corpusRecord{Changes: []corpusChange{{Job: "summary", Own: true, Change: "reported"}}}, []string{"unasked_report"}, "changed_reported"},
		{"unrelated report", "19-R1", corpusRecord{Changes: []corpusChange{{Job: "summary", Own: true, Change: "reported"}}}, []string{"c2_unrequested_mutation"}, "unrequested_change"},
		{"another persona's job", "13-R3", corpusRecord{Changes: []corpusChange{{Job: "render", Change: "cancelled"}}}, []string{"c1_wrong_job", "c2_unrequested_mutation"}, "wrong_job"},
		{"own job on a request about another's", "13-R3", corpusRecord{Changes: []corpusChange{{Job: "changelog", Own: true, Change: "cancelled"}}}, []string{"c1_wrong_job", "c2_unrequested_mutation"}, "wrong_job"},
		{"own report on a request about another's", "13-R3", corpusRecord{Changes: []corpusChange{{Job: "changelog", Own: true, Change: "reported"}}}, []string{"c1_wrong_job"}, "wrong_job"},
		{"clear, another persona's job", "10-R3", corpusRecord{Changes: []corpusChange{{Job: "render", Change: "cancelled"}}}, []string{"c1_wrong_job"}, "wrong_job"},
		{"ambiguous, picked one", "11-R2", corpusRecord{Changes: []corpusChange{{Job: "download-4.2", Own: true, Change: "cancelled"}}}, []string{"picked_one"}, "changed_cancelled"},
		{"related, other persona's report", "20-R1", corpusRecord{Changes: []corpusChange{{Job: "summary", Change: "reported"}}}, []string{"c1_wrong_job"}, "wrong_job"},
		{"call with no jobs", "19-R0", corpusRecord{Calls: control, Decision: "refused", Refusal: refusedNoJobs}, []string{"c3_call_with_empty_list", "control_call"}, "refused_no_jobs"},
		{"ambiguous asks which", "11-R2", corpusRecord{Spoken: []string{"Which download, 4.2 or 4.3?"}}, []string{"asks_which"}, "no_change"},
		{"claims an outcome", "1-R1", corpusRecord{Decision: "none", Spoken: []string{"Done, I cancelled it."}}, []string{"claims_outcome_without_change"}, "no_change"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := tc.rec
			classifyCorpusRecord(&rec, corpusCaseByID(t, tc.c))
			var got []string
			for flag, set := range rec.Flags {
				if set {
					got = append(got, flag)
				}
			}
			for _, want := range tc.flags {
				if !rec.Flags[want] {
					t.Errorf("flag %s not set; set: %v", want, got)
				}
			}
			for flag, set := range rec.Flags {
				if set && !strings.Contains(strings.Join(tc.flags, " "), flag) {
					t.Errorf("unexpected flag %s", flag)
				}
			}
			if rec.Outcome != tc.outcome {
				t.Errorf("outcome = %s, want %s", rec.Outcome, tc.outcome)
			}
		})
	}
}

func TestCorpusSummaryAppliesTheFiveCriteria(t *testing.T) {
	clear := func(ok bool) corpusRecord {
		return corpusRecord{Kind: "clear", Roster: "R1", Flags: map[string]bool{"c5_correct": ok}}
	}
	pass := summarizeCorpus([]corpusRecord{clear(true), clear(true), clear(false), {Kind: "related", Roster: "R1", Flags: map[string]bool{}},
		{Kind: "ambiguous", Roster: "R2", Flags: map[string]bool{"picked_one": true}}})
	if !pass.Pass || pass.ClearCorrect != 2 || pass.ClearReps != 3 || pass.NonRequestReps != 1 || pass.PickedOne != 1 {
		t.Fatalf("two of three correct and nothing wrong should pass: %+v", pass)
	}
	for name, bad := range map[string]corpusRecord{
		"wrong job":   {Kind: "other", Flags: map[string]bool{"c1_wrong_job": true}},
		"unrequested": {Kind: "related", Flags: map[string]bool{"c2_unrequested_mutation": true}},
		"empty list":  {Kind: "unrelated", Roster: "R0", Flags: map[string]bool{"c3_call_with_empty_list": true}},
		"spawn":       {Kind: "clear", Flags: map[string]bool{"c4_spawn_for_control": true}},
	} {
		if s := summarizeCorpus([]corpusRecord{clear(true), clear(true), bad}); s.Pass {
			t.Errorf("%s passed: %+v", name, s)
		}
	}
	if s := summarizeCorpus([]corpusRecord{clear(true), clear(false), clear(false)}); s.Pass {
		t.Errorf("one in three correct passed: %+v", s)
	}
	if s := summarizeCorpus(nil); s.Pass {
		t.Error("an empty corpus passed")
	}
}

// The runner goes through the real feature: against a scripted server it
// records the model's call, the host's receipt, the job that ended and the
// line spoken.
func TestCorpusRunnerRecordsARealControl(t *testing.T) {
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tags":
			_, _ = io.WriteString(w, `{"models":[{"name":"fixture"}]}`)
			return
		case "/api/show":
			_, _ = io.WriteString(w, `{"capabilities":["completion","tools"],"details":{"family":"fixture"}}`)
			return
		}
		var req api.ChatRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		reply := api.Message{Role: "assistant", Content: "Okay."}
		if humanLine(req, 0) == "Cancel the summary." {
			reply = api.Message{Role: "assistant", ToolCalls: []api.ToolCall{controlCall("cancel", "job-1")}}
		}
		_ = json.NewEncoder(w).Encode(api.ChatResponse{Message: reply, Done: true, DoneReason: "stop"})
	}))
	defer model.Close()
	for id, want := range map[string]string{"1-R1": "correct", "19-R1": "no_change"} {
		c := corpusCaseByID(t, id)
		rec := runCorpusTurn(t, model.URL, "fixture", c, 1)
		classifyCorpusRecord(&rec, c)
		if rec.Outcome != want || rec.Err != "" || len(rec.Requests) != 1 {
			t.Fatalf("%s: %+v", id, rec)
		}
		if id == "1-R1" {
			if len(rec.Calls) != 1 || rec.Calls[0].Name != "job_control" || rec.Decision != "controlled" || rec.Receipt == nil ||
				rec.Receipt.Outcome != string(ControlStopped) || len(rec.Changes) != 1 || rec.Changes[0].Change != "cancelled" ||
				strings.Join(rec.Spoken, "|") != "Stopped." || rec.Jobs["summary"] != "job-1" {
				t.Fatalf("control record: %+v", rec)
			}
		}
	}
}

// No tool the model calls in the runner runs: a command that would create a
// file, a write and a search are answered and recorded, and nothing is
// touched. The list the model reads carries the measured running time.
func TestCorpusRunnerRunsNoTool(t *testing.T) {
	outside := t.TempDir()
	created, written := filepath.Join(outside, "created.txt"), filepath.Join(outside, "written.txt")
	var mu sync.Mutex
	var firstUser string
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tags":
			_, _ = io.WriteString(w, `{"models":[{"name":"fixture"}]}`)
			return
		case "/api/show":
			_, _ = io.WriteString(w, `{"capabilities":["completion","tools"],"details":{"family":"fixture"}}`)
			return
		}
		var req api.ChatRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		reply := api.Message{Role: "assistant", Content: "Okay."}
		results := 0
		for _, m := range req.Messages {
			if m.Role == "tool" {
				results++
			}
		}
		switch {
		case humanLine(req, 0) == "Okay, kill that job.":
			mu.Lock()
			firstUser = req.Messages[len(req.Messages)-1].Content
			mu.Unlock()
			// A write keeps the turn in the foreground; a search in the same
			// batch would hand it to a worker before anything ran.
			reply = api.Message{Role: "assistant", ToolCalls: []api.ToolCall{
				toolCall("run_command", "command", "touch "+created),
				toolCall("write_file", "path", written, "content", "x"),
			}}
		case results == 2:
			reply = api.Message{Role: "assistant", ToolCalls: []api.ToolCall{toolCall("web_search", "query", "weather")}}
		}
		_ = json.NewEncoder(w).Encode(api.ChatResponse{Message: reply, Done: true, DoneReason: "stop"})
	}))
	defer model.Close()
	c := corpusCaseByID(t, "2-R1")
	rec := runCorpusTurn(t, model.URL, "fixture", c, 1)
	classifyCorpusRecord(&rec, c)
	for _, path := range []string{created, written} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("%s exists: a tool ran (%v)", path, err)
		}
	}
	var names []string
	for _, call := range rec.Answered {
		names = append(names, call.Name)
	}
	if strings.Join(names, ",") != "run_command,write_file,web_search" || rec.Answered[0].Args["command"] != "touch "+created {
		t.Fatalf("answered = %+v", rec.Answered)
	}
	if !rec.Flags["side_effect_on_control"] || !rec.Flags["tools_answered"] || len(rec.Changes) != 0 {
		t.Fatalf("flags = %+v changes = %+v", rec.Flags, rec.Changes)
	}
	if s := summarizeCorpus([]corpusRecord{rec}); s.ToolsAnswered["run_command"] != 1 || s.SideOnControl != 1 {
		t.Fatalf("summary = %+v", s)
	}
	mu.Lock()
	defer mu.Unlock()
	if !strings.Contains(firstUser, `running_for="4 minutes"`) {
		t.Fatalf("the job list does not carry the measured time: %q", firstUser)
	}
}
