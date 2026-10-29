package tui

import (
	"strings"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/chatmedia"

	"github.com/charmbracelet/x/ansi"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/pipeline"
)

func formatWorkerDetail(item pipeline.WorkerInfo, width int, expanded bool, previews ...func([]chatmedia.Ref, int, int) string) string {
	width = max(1, width)
	var blocks []string
	hasReply := false
	for _, entry := range workerThreadEntries(item) {
		switch {
		case entry.tool != nil:
			blocks = append(blocks, renderWorkerTool(entry.tool, width, expanded))
		case entry.role == "user":
			blocks = append(blocks, renderUserBubble("Request", ansi.Wrap(safeWorkerText(entry.text), max(1, width-userBubbleStyle.GetHorizontalFrameSize()), "")))
		case entry.role == "assistant":
			blocks = append(blocks, renderAgentTurnAtWidth("Worker", safeWorkerText(entry.text), max(1, width-agentBubbleStyle.GetHorizontalFrameSize())))
			hasReply = true
		}
		if len(entry.attachments) > 0 && len(previews) > 0 {
			blocks = append(blocks, previews[0](entry.attachments, width, 8))
		}
	}
	if item.Activity != "" && len(item.Tools) == 0 {
		activity := dimStyle.Render(wrapWorkerPlain("⚙ "+workerActivitySummary(item.Activity), width))
		if expanded {
			activity += "\n" + formatWorkerPayload(item.Activity, width)
		}
		blocks = append(blocks, activity)
	}
	if item.Err != "" {
		blocks = append(blocks, errorStyle.Render(wrapWorkerPlain("Error: "+item.Err, width)))
	}
	if item.Termination != "" {
		blocks = append(blocks, dimStyle.Render(wrapWorkerPlain("Termination: "+item.Termination, width)))
	}
	status := ""
	switch item.State {
	case "queued":
		status = "◌ Queued"
	case "running":
		status = "● Working…"
	case "stopping":
		status = "◌ Stopping…"
	default:
		if !hasReply && item.Err == "" {
			status = "No worker reply was recorded."
		}
	}
	if status != "" {
		blocks = append(blocks, dimStyle.Render(wrapWorkerPlain(status, width)))
	}
	if expanded {
		blocks = append(blocks, dimStyle.Render(wrapWorkerPlain("Task ID: "+item.ID, width)))
	}
	return strings.Join(blocks, "\n")
}

func renderWorkerTool(tool *workerThreadTool, width int, expanded bool) string {
	call := tool.call
	name := compactWorkerText(call.Name)
	if name == "" {
		name = "Tool"
	}
	label := "⚙ " + name
	if call.Status != "" && call.Status != "unknown" {
		label += " · " + compactWorkerText(call.Status)
	}
	elapsed := call.Elapsed
	if call.Status == "running" && !call.Started.IsZero() {
		elapsed = time.Since(call.Started)
	}
	if elapsed >= time.Second {
		label += " · " + roundSeconds(elapsed)
	}
	style := dimStyle
	if call.Status == "running" {
		style = warningStyle
	} else if call.Err != "" || call.Status == "failed" {
		style = errorStyle
	}
	lines := []string{style.Render(wrapWorkerPlain(label, width))}
	if call.Summary != "" {
		summary := ansi.Truncate(compactWorkerText(call.Summary), width, "…")
		if expanded {
			summary = wrapWorkerPlain(call.Summary, width)
		}
		lines = append(lines, dimStyle.Render(summary))
	}
	if expanded {
		if call.Status == "" || call.Status == "unknown" {
			lines = append(lines, dimStyle.Render(wrapWorkerPlain("Outcome not reported", width)))
		}
		if tool.arguments != "" {
			lines = append(lines, sectionStyle.Render("Arguments"), formatWorkerPayload(tool.arguments, width))
		}
		// Prefer the observed preview, which has already been redacted by the
		// event projection, over a provider's raw result for the same call.
		result := call.Preview
		if result == "" {
			result = tool.result
		}
		if result != "" {
			lines = append(lines, sectionStyle.Render("Result"), formatWorkerPayload(result, width))
		}
	}
	// Failures stay visible even when payload details are collapsed.
	if call.Err != "" {
		lines = append(lines, errorStyle.Render(wrapWorkerPlain(call.Err, width)))
	}
	return strings.Join(lines, "\n")
}

func workerDetailMeta(item pipeline.WorkerInfo) string {
	meta := workerStateStyle(item.State).Render(safeWorkerText(item.State)) + " · " + metaStyle.Render(safeWorkerText(workerProvider(item)))
	meta += dimStyle.Render(" · " + roundSeconds(item.Elapsed))
	if item.PersonaName != "" {
		meta += dimStyle.Render(" · " + compactWorkerText(item.PersonaName))
	}
	return meta
}

func workerDetailHelp(width int, expanded bool) (string, string) {
	if width < 36 {
		return "  ↑/↓ · d details", "  s/x · t · esc list"
	}
	if width < 62 {
		return "  ↑/↓ scroll · home/end · d details", "  s stop · x all · t workers · esc"
	}
	action := "show"
	if expanded {
		action = "hide"
	}
	return "  ↑/↓ scroll · home/end · d " + action + " tool details", "  s stop task · x stop all · t toggle workers · esc list"
}
