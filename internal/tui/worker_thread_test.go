package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/pipeline"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/workfold"
)

func workerChatFixture() pipeline.WorkerInfo {
	return pipeline.WorkerInfo{ID: "chat-fixture", Task: "Check the project", State: "running", Provider: "ollama", Model: "fixture-model",
		Thread: []brain.Turn{
			{Role: "system", Content: "private system instruction"},
			{Role: "user", Content: "Check the project"},
			{Role: "assistant", Content: "I will inspect the files, then run the tests.", ToolCalls: []brain.TurnToolCall{{ID: "read", Name: "list_files", Arguments: `{"path":"."}`}}},
			{Role: "tool", ToolCallID: "read", ToolName: "list_files", Content: "unabridged file listing"},
			{Role: "assistant", Content: "The project has a Go test suite.", ToolCalls: []brain.TurnToolCall{{ID: "tests", Name: "run_command", Arguments: `{"command":"go test ./..."}`}}},
		},
		Tools: []workfold.Call{
			{ID: "read", Name: "list_files", Summary: ".", Status: "unknown", Preview: "go.mod, main.go, main_test.go"},
			{ID: "tests", Name: "run_command", Summary: "go test ./...", Status: "running"},
		},
	}
}

func TestWorkerThreadCombinesMessagesAndCalls(t *testing.T) {
	item := workerChatFixture()
	item.State, item.Result = "done", "One check failed."
	item.Tools[1].Status, item.Tools[1].Err = "failed", "exit 1"
	item.Thread = append(item.Thread, brain.Turn{Role: "tool", ToolCallID: "tests", Content: "FAIL project checks"}, brain.Turn{Role: "assistant", Content: item.Result})
	for _, expanded := range []bool{false, true} {
		view := stripANSI(formatWorkerDetail(item, 80, expanded))
		ordered := []string{"› Request", "Check the project", "I will inspect", "⚙ list_files", "The project has", "⚙ run_command", "exit 1", "One check failed."}
		last := -1
		for _, want := range ordered {
			at := strings.Index(view, want)
			if at <= last {
				t.Fatalf("missing/out-of-order %q in expanded=%v:\n%s", want, expanded, view)
			}
			last = at
		}
		for _, once := range []string{"Check the project", "⚙ list_files", "⚙ run_command", "One check failed."} {
			if strings.Count(view, once) != 1 {
				t.Fatalf("duplicated %q:\n%s", once, view)
			}
		}
		if strings.Contains(view, "private system") || strings.Contains(view, "unabridged file listing") {
			t.Fatalf("system instructions or raw result replaced the projected preview:\n%s", view)
		}
		for _, detail := range []string{`"path"`, "go.mod, main.go", "Outcome not reported", "FAIL project checks"} {
			if strings.Contains(view, detail) != expanded {
				t.Fatalf("payload visibility %q expanded=%v:\n%s", detail, expanded, view)
			}
		}
	}
}

func TestWorkerThreadPreservesRepeatedCallsWithoutIDs(t *testing.T) {
	for _, ids := range []bool{false, true} {
		item := pipeline.WorkerInfo{Task: "Read two files"}
		for i, path := range []string{"first.txt", "second.txt"} {
			id := ""
			if ids {
				id = path
			}
			item.Thread = append(item.Thread,
				brain.Turn{Role: "assistant", ToolCalls: []brain.TurnToolCall{{ID: id, Name: "read_file"}}},
				brain.Turn{Role: "tool", ToolCallID: id, ToolName: "read_file", Content: path + " content"})
			item.Tools = append(item.Tools, workfold.Call{ID: id, Name: "read_file", Summary: path, Status: []string{"succeeded", "failed"}[i]})
		}
		entries := workerThreadEntries(item)
		if len(entries) != 3 || entries[1].tool.result != "first.txt content" || entries[2].tool.result != "second.txt content" || entries[2].tool.call.Status != "failed" {
			t.Fatalf("conflated repeated calls (IDs=%v): %+v", ids, entries)
		}
	}
	// A different ID must stay separate even if the tool name matches.
	item := pipeline.WorkerInfo{Thread: []brain.Turn{{Role: "assistant", ToolCalls: []brain.TurnToolCall{{ID: "a", Name: "read"}}}}, Tools: []workfold.Call{{ID: "b", Name: "read"}}}
	if len(workerThreadEntries(item)) != 2 {
		t.Fatal("matched different call IDs by name")
	}
}

func TestWorkerThreadWithoutHistoryPlacesCallsBeforeFinalReply(t *testing.T) {
	item := pipeline.WorkerInfo{Task: "Weather in Los Angeles", State: "done", Result: "It is 71°F.", Tools: []workfold.Call{
		{ID: "1", Name: "activate_skill", Summary: "obey-voice", Status: "unknown"},
		{ID: "2", Name: "run_command", Summary: "curl weather", Status: "succeeded"},
		{ID: "3", Name: "route", Summary: "chat", Status: "unknown"},
	}}
	for _, history := range []bool{false, true} {
		if history {
			item.Thread = []brain.Turn{{Role: "user", Content: item.Task}, {Role: "assistant", Content: item.Result}}
		}
		view := stripANSI(formatWorkerDetail(item, 80, false))
		if strings.Index(view, "activate_skill") > strings.Index(view, "run_command") || strings.Index(view, "route") > strings.Index(view, item.Result) || strings.Count(view, item.Result) != 1 {
			t.Fatalf("lost call order or duplicated result:\n%s", view)
		}
	}
}

func TestWorkerThreadMergesUnmatchedCallsAtKnownTimes(t *testing.T) {
	at := time.Now()
	item := pipeline.WorkerInfo{Thread: []brain.Turn{
		{Role: "user", Content: "Request", At: at},
		{Role: "assistant", Content: "Before", At: at.Add(time.Second)},
		{Role: "assistant", Content: "After", At: at.Add(3 * time.Second)},
	}, Tools: []workfold.Call{{Name: "inspect", Started: at.Add(2 * time.Second)}}}
	entries := workerThreadEntries(item)
	if len(entries) != 4 || entries[1].text != "Before" || entries[2].tool == nil || entries[3].text != "After" {
		t.Fatalf("call was not placed between timestamped replies: %+v", entries)
	}
}

func TestWorkerThreadDoesNotConfuseProgressWithFinalReply(t *testing.T) {
	item := pipeline.WorkerInfo{Task: "Retry the check", Result: "The check failed.", Thread: []brain.Turn{
		{Role: "assistant", Content: "The check failed.", ToolCalls: []brain.TurnToolCall{{ID: "retry", Name: "run_command"}}},
		{Role: "tool", ToolCallID: "retry", Content: "exit 1"},
	}}
	entries := workerThreadEntries(item)
	if len(entries) != 4 || entries[3].role != "assistant" || entries[3].text != item.Result {
		t.Fatalf("progress swallowed the final reply: %+v", entries)
	}
}

func TestWorkerThreadKeepsFailuresAndTerminationVisible(t *testing.T) {
	item := pipeline.WorkerInfo{State: "aborted", Err: "worker disconnected", Termination: "unknown", Result: "Partial findings"}
	view := stripANSI(formatWorkerDetail(item, 80, false))
	for _, want := range []string{"Partial findings", "Error: worker disconnected", "Termination: unknown"} {
		if !strings.Contains(view, want) {
			t.Fatalf("missing %q: %s", want, view)
		}
	}
}

func TestWorkerThreadWrapsBubblesWithoutBreakingThinkTags(t *testing.T) {
	item := workerChatFixture()
	item.Thread = append(item.Thread, brain.Turn{Role: "assistant", Content: "<think>" + strings.Repeat("界", 60) + "</think>\nReply\x1b[2J"})
	for _, width := range []int{16, 36, 76, 116} {
		for _, expanded := range []bool{false, true} {
			view := formatWorkerDetail(item, width, expanded)
			for _, line := range strings.Split(view, "\n") {
				if lipgloss.Width(line) > width {
					t.Fatalf("overflow at %d: %q", width, line)
				}
			}
			if strings.Contains(view, "<think>") || strings.Contains(view, "</think>") || strings.Contains(view, "\x1b[2J") || !strings.Contains(stripANSI(view), "Reply") {
				t.Fatalf("unsafe or broken reply at %d: %s", width, view)
			}
		}
	}
}
