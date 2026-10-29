package brain

import (
	"fmt"
	"strings"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

// The public app-server item envelope supplies concrete activity and terminal
// facts. Decode only the diagnostic fields we display, not arbitrary arguments.
type codexToolNotice struct {
	ID               string `json:"id"`
	Type             string `json:"type"`
	Status           string `json:"status"`
	Command          string `json:"command"`
	Cwd              string `json:"cwd"`
	AggregatedOutput string `json:"aggregatedOutput"`
	ExitCode         *int   `json:"exitCode"`
	DurationMs       *int64 `json:"durationMs"`
	Query            string `json:"query"`
	Path             string `json:"path"`
	Tool             string `json:"tool"`
	Server           string `json:"server"`
	Changes          []struct {
		Path string `json:"path"`
	} `json:"changes"`
}

func (n codexToolNotice) call() ToolCall {
	call := ToolCall{ID: n.ID, Name: n.Type, Kind: KindOther}
	switch n.Type {
	case "commandExecution":
		call.Kind, call.Summary = KindExecute, n.Command
		if n.Cwd != "" {
			call.Summary += " (in " + n.Cwd + ")"
		}
	case "fileChange":
		call.Kind = KindWrite
		paths := make([]string, 0, len(n.Changes))
		for _, change := range n.Changes {
			paths = append(paths, change.Path)
		}
		call.Summary = strings.Join(paths, ", ")
	case "webSearch":
		call.Kind, call.Summary = KindWebSearch, n.Query
	case "imageView":
		call.Kind, call.Summary = KindRead, n.Path
	case "mcpToolCall", "dynamicToolCall":
		call.Summary = strings.TrimSpace(n.Server + " " + n.Tool)
	case "collabAgentToolCall":
		call.Kind, call.Summary = KindTask, n.Tool
	}
	if strings.TrimSpace(call.Summary) == "" {
		call.Summary = "Codex " + n.Type
	}
	call.Summary = redactToolText(call.Summary, 240)
	return call
}

func (n codexToolNotice) result(elapsed time.Duration) ToolResult {
	if n.DurationMs != nil && *n.DurationMs >= 0 {
		elapsed = time.Duration(*n.DurationMs) * time.Millisecond
	}
	outcome := &events.ToolOutcome{Status: events.ToolOutcomeUnknown, Elapsed: elapsed,
		ExitCode: n.ExitCode, InputSummary: n.call().Summary,
		OutputPreview: redactToolText(n.AggregatedOutput, 240), OutputBytes: int64(len(n.AggregatedOutput)),
		Truncated: len([]rune(n.AggregatedOutput)) > 240}
	switch n.Status {
	case "completed":
		outcome.Status = events.ToolOutcomeSucceeded
	case "failed", "declined":
		outcome.Status, outcome.Reason = events.ToolOutcomeFailed, "Codex tool "+n.Status
	}
	if n.ExitCode != nil && *n.ExitCode != 0 {
		outcome.Status, outcome.Reason = events.ToolOutcomeFailed, fmt.Sprintf("Command exited with code %d", *n.ExitCode)
	}
	return ToolResult{ToolCall: n.call(), Preview: outcome.OutputPreview, Err: outcome.Reason, Elapsed: elapsed, Outcome: outcome}
}
