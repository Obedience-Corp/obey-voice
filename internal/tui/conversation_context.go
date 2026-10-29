package tui

import (
	"fmt"
	"math"
	"strings"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

func (m conversationModel) contextMeterHeight() int {
	if m.height < 12 || m.width < 20 {
		return 0
	}
	return 1
}

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

func (m conversationModel) contextMeter() string {
	if m.contextMeterHeight() == 0 {
		return ""
	}
	window := m.contextBudget.NumCtx
	if window <= 0 && m.cfg != nil && (m.cfg.BrainProvider == "ollama" || m.cfg.BrainProvider == "") {
		window = m.cfg.OllamaNumCtx
	}
	used, capacity := "—", "—"
	if window > 0 {
		capacity = contextTokens(window)
	}
	if m.contextKnown {
		used = contextTokens(m.contextBudget.EstTokens)
	}
	label := "Context " + used + " / " + capacity
	style := dimStyle
	if m.contextKnown {
		soft, hard := m.contextBudget.SoftLimit, m.contextBudget.HardLimit
		if soft <= 0 {
			soft = window * 8 / 10
		}
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
	width := max(1, m.width-4)
	barWidth := min(24, width-lipgloss.Width(label)-3)
	if barWidth > 3 {
		fill := int(math.Round(m.contextFill * float64(barWidth)))
		bar := strings.Repeat("━", fill) + strings.Repeat("─", barWidth-fill)
		if m.compactStage == "summarize" && !m.reducedMotion {
			at := m.voiceFrame % barWidth
			bar = strings.Repeat("─", at) + "━" + strings.Repeat("─", barWidth-at-1)
		}
		label += " [" + bar + "]"
	}
	return "  " + style.Render(ansi.Truncate(label, width, "…"))
}
