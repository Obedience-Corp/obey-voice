package tui

import (
	"context"
	"errors"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	tea "github.com/charmbracelet/bubbletea"
	"strings"
	"testing"
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
	if !strings.Contains(m.message, "Needs install or repair") || !strings.Contains(m.message, cfg.ModelsDir) {
		t.Fatal(m.message)
	}
}
