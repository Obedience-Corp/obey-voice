package config

import "testing"

func TestClaudeModelSpellingsShareOneValue(t *testing.T) {
	for in, want := range map[string]string{
		"":                    "",
		"default":             "",
		" Default ":           "",
		" haiku ":             "haiku",
		"claude-Sonnet-4-5-x": "claude-Sonnet-4-5-x",
	} {
		cfg := &Config{}
		SetBrainModel(cfg, "claude", in)
		if cfg.ClaudeModel != want || BrainModelFor(&Config{ClaudeModel: in}, "claude") != want {
			t.Errorf("%q: stored %q, read %q; want %q", in, cfg.ClaudeModel, BrainModelFor(&Config{ClaudeModel: in}, "claude"), want)
		}
	}
}
