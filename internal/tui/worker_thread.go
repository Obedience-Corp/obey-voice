package tui

import (
	"slices"
	"strings"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/chatmedia"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/pipeline"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/workfold"
)

// A thread entry borrows only immutable UI snapshots. Provider history supplies
// message order; observed calls supply outcomes without inferring success from
// the presence of a tool result.
type workerThreadEntry struct {
	role, text  string
	attachments []chatmedia.Ref
	at          time.Time
	tool        *workerThreadTool
}

type workerThreadTool struct {
	call      workfold.Call
	arguments string
	result    string
	returned  bool
	order     int // index in the observed call sequence; -1 if history only
}

func workerThreadEntries(item pipeline.WorkerInfo) []workerThreadEntry {
	var entries []workerThreadEntry
	used := make([]bool, len(item.Tools))
	hasRequest := slices.ContainsFunc(item.Thread, func(t brain.Turn) bool {
		return t.Role == "user" && strings.TrimSpace(t.Content) != ""
	})
	if !hasRequest && item.Task != "" {
		entries = append(entries, workerThreadEntry{role: "user", text: item.Task})
	}
	for _, turn := range item.Thread {
		switch turn.Role {
		case "user", "assistant":
			if strings.TrimSpace(turn.Content) != "" || len(turn.Attachments) > 0 {
				entries = append(entries, workerThreadEntry{role: turn.Role, text: turn.Content, at: turn.At, attachments: turn.Attachments})
			}
			for _, call := range turn.ToolCalls {
				tool := &workerThreadTool{call: workfold.Call{ID: call.ID, Name: call.Name, Status: "unknown"}, arguments: call.Arguments, order: -1}
				matchWorkerCall(tool, item.Tools, used)
				entries = append(entries, workerThreadEntry{tool: tool, at: turn.At})
			}
		case "tool":
			tool := findWorkerTool(entries, turn)
			if tool == nil {
				tool = &workerThreadTool{call: workfold.Call{ID: turn.ToolCallID, Name: turn.ToolName, Status: "unknown"}, order: -1}
				matchWorkerCall(tool, item.Tools, used)
				entries = append(entries, workerThreadEntry{tool: tool, at: turn.At})
			}
			tool.result, tool.returned = turn.Content, true
		}
	}

	// Only a reply at the end of the thread can stand in for the final result.
	// Identical progress before a tool call must not swallow the final reply.
	final := -1
	if len(entries) > 0 && strings.TrimSpace(item.Result) != "" {
		last := len(entries) - 1
		if entries[last].role == "assistant" && strings.TrimSpace(entries[last].text) == strings.TrimSpace(item.Result) {
			final = last
		}
	}
	if strings.TrimSpace(item.Result) != "" && final < 0 {
		final = len(entries)
		entries = append(entries, workerThreadEntry{role: "assistant", text: item.Result, at: item.Ended})
	}

	// CLI/managed providers may report calls without history tool turns. Place
	// them using known timestamps or the next matched call. Without either,
	// retain arrival order before the final reply (or after current progress).
	next := 0
	for i, call := range item.Tools {
		if used[i] {
			continue
		}
		at := len(entries)
		for j := next; j < len(entries); j++ {
			e := entries[j]
			if (e.tool != nil && e.tool.order > i) ||
				(e.role != "user" && !call.Started.IsZero() && !e.at.IsZero() && call.Started.Before(e.at)) || j == final {
				at = j
				break
			}
		}
		entry := workerThreadEntry{tool: &workerThreadTool{call: call, order: i}, at: call.Started}
		entries = slices.Insert(entries, at, entry)
		if final >= at {
			final++
		}
		next = at + 1
	}
	return entries
}

func matchWorkerCall(tool *workerThreadTool, calls []workfold.Call, used []bool) {
	for i, call := range calls {
		if used[i] {
			continue
		}
		if tool.call.ID != "" && call.ID != "" {
			if tool.call.ID != call.ID {
				continue
			}
		} else if tool.call.Name == "" || tool.call.Name != call.Name {
			continue
		}
		tool.call, tool.order, used[i] = call, i, true
		return
	}
}

func findWorkerTool(entries []workerThreadEntry, turn brain.Turn) *workerThreadTool {
	// Results without IDs occur in older/provider-specific histories. Match
	// the first unfinished call of the same name; never conflate distinct IDs.
	for _, entry := range entries {
		tool := entry.tool
		if tool == nil || tool.returned {
			continue
		}
		if turn.ToolCallID != "" && tool.call.ID != "" {
			if turn.ToolCallID == tool.call.ID {
				return tool
			}
		} else if turn.ToolName != "" && turn.ToolName == tool.call.Name {
			return tool
		}
	}
	return nil
}
