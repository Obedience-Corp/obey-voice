package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

func TestDecisionSetupDoesNotEnableAndFencesCanceledWork(t *testing.T) {
	cfg := &config.Config{ModelsDir: t.TempDir(), DecisionProvider: "openai", DecisionLocalModel: "2b"}
	m := newSettings(cfg, nil)
	m.decisionSetup.ensure = func(ctx context.Context, c *config.Config, r config.AssetRequest, p func(config.AssetProgress)) error {
		if c == cfg || !config.ManagedDecisions(c) || !r.NeedDecision || r.NeedTTS {
			t.Fatal("wrong setup scope")
		}
		p(config.AssetProgress{Asset: "fixture", Phase: "download", Pct: 50})
		return ctx.Err()
	}
	batch := m.startDecisionSetup(true)().(tea.BatchMsg)
	ctx := m.decisionSetup.ctx
	seq := m.decisionSetup.seq
	m.closePreview()
	done := batch[1]()
	if !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatal("setup was not canceled")
	}
	m, _ = m.Update(done)
	if cfg.DecisionEnabled || cfg.DecisionProvider != "openai" || m.decisionSetup.running {
		t.Fatal("setup changed routing")
	}
	_, cmd := m.Update(decisionSetupProgressMsg{seq: seq})
	if cmd != nil {
		t.Fatal("stale progress accepted")
	}
	batch = m.startDecisionSetup(false)().(tea.BatchMsg)
	m, _ = m.Update(batch[1]())
	if !strings.Contains(m.decisionSetup.report, "Needs install or repair") || !strings.Contains(m.decisionSetup.report, cfg.ModelsDir) {
		t.Fatal(m.decisionSetup.report)
	}
}

func TestDecisionSetupReportReadableInRenderedTerminal(t *testing.T) {
	for _, state := range []string{"Installed and verified", "Needs install or repair: model weights"} {
		t.Run(state, func(t *testing.T) {
			m := newSettings(&config.Config{DecisionLocalModel: "2b"}, nil)
			m.section, m.width, m.height = sectionBrain, 80, 24
			lines := []string{"Decider 2B (default)", "3.5 GiB download · Apache-2.0", "Location: /Volumes/fast/decision-models", state}
			for i := 0; i < 30; i++ {
				lines = append(lines, fmt.Sprintf("Additional setup requirement %02d", i))
			}
			m.decisionSetup.running = true
			m, _ = m.Update(decisionSetupDoneMsg{seq: m.decisionSetup.seq, report: strings.Join(lines, "\n")})
			var frames strings.Builder
			for i := 0; i < 100; i++ {
				view := ansi.Strip(m.View())
				if len(strings.Split(view, "\n")) > 24 {
					t.Fatal("report escaped terminal height")
				}
				for _, line := range strings.Split(view, "\n") {
					if ansi.StringWidth(line) > 80 {
						t.Fatal("report escaped terminal width")
					}
				}
				frames.WriteString(view)
				frames.WriteByte('\n')
				before := m.cursor
				m, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
				if m.cursor == before {
					break
				}
			}
			for _, line := range lines {
				if !strings.Contains(frames.String(), line) {
					t.Errorf("not visible while scrolling: %q", line)
				}
			}
			// A changed selection invalidates the displayed verification and rejects
			// a late result from the previous setup generation.
			old := m.decisionSetup.seq
			m.saveConfig = func(string, any) error { return nil }
			m.saveDecision("decision_local_model", "d1-3b")
			m, _ = m.Update(decisionSetupDoneMsg{seq: old, report: "STALE REPORT"})
			if m.decisionSetup.report != "" || strings.Contains(m.View(), "STALE REPORT") {
				t.Fatal("old selection report survived")
			}
		})
	}
}

func TestDecisionSetupErrorShownInReport(t *testing.T) {
	m := newSettings(&config.Config{}, nil)
	m.section, m.width, m.height = sectionBrain, 80, 24
	m.decisionSetup.running = true
	m, _ = m.Update(decisionSetupDoneMsg{seq: m.decisionSetup.seq, err: errors.New("checksum mismatch")})
	var frames string
	for i := 0; i < 12; i++ {
		frames += ansi.Strip(m.View())
		m, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	}
	for _, want := range []string{"checksum mismatch", "Choose Install / repair to retry."} {
		if !strings.Contains(frames, want) {
			t.Fatalf("error report hid %q", want)
		}
	}
}

func TestDecisionSetupReportReflowsLongLocation(t *testing.T) {
	m := newSettings(&config.Config{}, nil)
	m.section, m.width, m.height = sectionBrain, 80, 24
	report := "Location: /Volumes/fast/" + strings.Repeat("long-model-directory/", 8) + "模型\nInstalled and verified"
	m.decisionSetup.running = true
	m, _ = m.Update(decisionSetupDoneMsg{seq: m.decisionSetup.seq, report: report})
	for _, width := range []int{50, 100, 80} {
		m, _ = m.Update(tea.WindowSizeMsg{Width: width, Height: 24})
		m.cursor = lineOf(m, "decision_setup_report_0", -1)
		var rendered strings.Builder
		for i := 0; i < 100; i++ {
			row, ok := m.focusedRow()
			if !ok || row.kind != rowReport {
				break
			}
			found := false
			for _, line := range strings.Split(ansi.Strip(m.View()), "\n") {
				if _, text, ok := strings.Cut(line, "▸ "); ok {
					rendered.WriteString(text)
					found = true
					break
				}
			}
			if !found {
				t.Fatal("focused report line is not visible")
			}
			before := m.cursor
			m, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
			if before == m.cursor {
				break
			}
		}
		compact := func(s string) string { return strings.Join(strings.Fields(s), "") }
		if compact(rendered.String()) != compact(report) {
			t.Fatalf("width %d lost report text: %q", width, rendered.String())
		}
	}
}
