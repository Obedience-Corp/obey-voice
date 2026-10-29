package tui

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

func TestContextMeterPressureProgressAndClear(t *testing.T) {
	m := sizedConversation(t, 90, 24)
	m.cfg = &config.Config{BrainProvider: "ollama", OllamaNumCtx: 32768}
	if !strings.Contains(stripANSI(m.View()), "Context — / 32k") {
		t.Fatal("missing idle meter")
	}
	m.handleEvent(events.ContinuityBudget{EstTokens: 7424, NumCtx: 8192, SoftLimit: 6553, HardLimit: 7372})
	if !strings.Contains(stripANSI(m.View()), "7.4k / 8k") || m.contextTarget() < .9 {
		t.Fatal("missing pressure estimate")
	}
	m.handleEvent(events.CompactionProgress{Stage: "summarize"})
	if !strings.Contains(stripANSI(m.View()), "compact summarize") || !m.shouldAnimateVoice() {
		t.Fatal("missing compact progress")
	}
	m.handleEvent(events.ContinuityBudget{EstTokens: 1000, NumCtx: 8192})
	m.handleEvent(events.CompactionProgress{Stage: "complete", Elapsed: time.Second})
	if m.compactStage != "" || m.contextFill != m.contextTarget() {
		t.Fatal("completion did not snap meter")
	}
	for _, stage := range []string{"skipped", "canceled", "failed"} {
		m.handleEvent(events.CompactionProgress{Stage: "summarize"})
		m.handleEvent(events.CompactionProgress{Stage: stage})
		if m.compactStage != "" {
			t.Fatal("terminal stage stuck")
		}
	}
	m.handleEvent(events.ConversationCleared{})
	if m.contextKnown {
		t.Fatal("clear retained old budget")
	}
	for _, size := range [][2]int{{80, 24}, {40, 12}, {20, 8}} {
		m.setSize(size[0], size[1])
		v := m.View()
		if lipgloss.Height(v) > size[1] {
			t.Fatalf("view exceeds %v: %d", size, lipgloss.Height(v))
		}
		for _, line := range strings.Split(v, "\n") {
			if lipgloss.Width(line) > size[0] {
				t.Fatalf("line exceeds width %d", size[0])
			}
		}
	}
}

func TestMemorySaveFailurePreservesLiveConfig(t *testing.T) {
	cfg := &config.Config{BrainProvider: "ollama", OllamaNumCtx: 8192, ContinuityNumCtxCeil: 16384}
	m := newSettings(cfg, nil)
	m.saveMemory = func(int) error { return errors.New("fixture write failure") }
	row := m.memoryRows()[0]
	row.set(&m, 2)
	if cfg.OllamaNumCtx != 8192 || cfg.ContinuityNumCtxCeil != 16384 || !strings.Contains(m.message, "not saved") {
		t.Fatal("failed save changed live config")
	}
	m, _ = m.Update(tea.WindowSizeMsg{Width: 90, Height: 24})
}
