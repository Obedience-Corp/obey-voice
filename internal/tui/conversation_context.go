package tui

import (
	"fmt"
	"math"
	"strings"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

func (m conversationModel) contextTarget() float64 {
	if m.contextBudget.NumCtx <= 0 {
		return 0
	}
	return min(1, max(0, float64(m.contextBudget.EstTokens)/float64(m.contextBudget.NumCtx)))
}

func (m *conversationModel) advanceContextMeter() {
	target := m.contextTarget()
	if m.reducedMotion || math.Abs(target-m.contextFill) < .005 {
		m.contextFill = target
	} else {
		m.contextFill += (target - m.contextFill) * .4
	}
}

func (m *conversationModel) handleContextBudget(e events.ContinuityBudget) {
	if e.NumCtx <= 0 {
		return
	}
	first := !m.contextKnown
	m.contextBudget = e
	m.contextKnown = true
	if first || m.reducedMotion {
		m.contextFill = m.contextTarget()
	}
}

func contextTokens(n int) string {
	if n >= 1024 && n%1024 == 0 {
		return fmt.Sprintf("%dk", n/1024)
	}
	if n >= 1000 {
		return fmt.Sprintf("%.1fk", float64(n)/1000)
	}
	return fmt.Sprint(n)
}

// composerLabel keeps memory beside the input status without taking a row from
// the conversation. Narrow terminals shorten the meter before the input status.
func (m conversationModel) composerLabel(status string, width int) string {
	if width < 32 {
		return ansi.Truncate(status, width, "…")
	}
	meter := m.contextMeter(min(60, max(16, width-lipgloss.Width(status)-2)))
	if meter == "" {
		return ansi.Truncate(status, width, "…")
	}
	status = ansi.Truncate(status, max(1, width-lipgloss.Width(meter)-2), "…")
	return status + strings.Repeat(" ", max(2, width-lipgloss.Width(status)-lipgloss.Width(meter))) + meter
}

func (m conversationModel) contextMeter(width int) string {
	if width <= 0 {
		return ""
	}
	window := m.contextBudget.NumCtx
	if window <= 0 && m.cfg != nil && (m.cfg.BrainProvider == "ollama" || m.cfg.BrainProvider == "") {
		window = m.cfg.OllamaNumCtx
	}
	used, capacity := "waiting", "—"
	if window > 0 {
		capacity = contextTokens(window)
	}
	if m.contextKnown {
		used = "~" + contextTokens(m.contextBudget.EstTokens)
	}
	label := "Context " + used + " / " + capacity
	if !m.contextKnown && window <= 0 {
		label = "Context unavailable"
	}
	style := dimStyle
	if m.contextKnown {
		style = lipgloss.NewStyle().Foreground(colorAccent)
		soft, hard := m.contextBudget.SoftLimit, m.contextBudget.HardLimit
		if soft <= 0 {
			soft = window * 8 / 10
		}
		label += fmt.Sprintf(" · %.0f%%", 100*float64(m.contextBudget.EstTokens)/float64(window))
		if hard <= 0 {
			hard = window * 9 / 10
		}
		switch {
		case m.contextBudget.EstTokens >= hard:
			style = errorStyle
		case m.contextBudget.EstTokens >= soft:
			style = warningStyle
		}
	}
	if m.compactStage != "" {
		label += " · compact " + m.compactStage
		style = thinkStyle
	}
	// No empty bar while waiting: absence of telemetry is not zero usage.
	barWidth := min(12, width-lipgloss.Width(label)-3)
	if barWidth >= 6 && m.contextKnown {
		fill := min(barWidth, max(0, int(math.Round(m.contextFill*float64(barWidth)))))
		bar := strings.Repeat("━", fill)
		if fill == 0 && m.contextFill > 0 {
			bar = "╸" // Keep a small positive usage visibly different from zero.
		}
		bar += dimStyle.Render(strings.Repeat("─", barWidth-lipgloss.Width(bar)))
		if m.compactStage == "summarize" && !m.reducedMotion {
			at := m.voiceFrame % barWidth
			bar = strings.Repeat("─", at) + "━" + strings.Repeat("─", barWidth-at-1)
		}
		label += " [" + bar + "]"
	}
	if lipgloss.Width(label) > width {
		switch {
		case m.compactStage != "":
			label = "compact " + m.compactStage
		case m.contextKnown:
			label = fmt.Sprintf("Context %.0f%%", 100*float64(m.contextBudget.EstTokens)/float64(window))
		}
	}
	return style.Render(ansi.Truncate(label, width, "…"))
}
