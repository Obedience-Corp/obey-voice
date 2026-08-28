package scene

import (
	"testing"
	"time"
)

func validOptions() Options {
	return Options{Script: "script.yaml", OutDir: "out/scene"}
}

func TestSceneOptionsValidateRejects(t *testing.T) {
	cases := map[string]Options{
		"no script":            {OutDir: "out/scene"},
		"blank script":         {Script: "  ", OutDir: "out/scene"},
		"no out-dir":           {Script: "script.yaml"},
		"blank out-dir":        {Script: "script.yaml", OutDir: " "},
		"unsupported mode":     {Script: "s.yaml", OutDir: "out", Mode: "improvise", ModeSet: true},
		"unparsable pause":     {Script: "s.yaml", OutDir: "out", Pause: "soon", PauseSet: true},
		"negative pause":       {Script: "s.yaml", OutDir: "out", Pause: "-1s", PauseSet: true},
		"zero max-turns":       {Script: "s.yaml", OutDir: "out", MaxTurns: 0, MaxTurnsSet: true},
		"max-turns over cap":   {Script: "s.yaml", OutDir: "out", MaxTurns: MaxTurns + 1, MaxTurnsSet: true},
		"negative max-turns":   {Script: "s.yaml", OutDir: "out", MaxTurns: -1, MaxTurnsSet: true},
		"unsupported encoding": {Script: "s.yaml", OutDir: "out", AudioFormat: "flac"},
	}
	for name, opts := range cases {
		t.Run(name, func(t *testing.T) {
			if err := opts.Validate(); err == nil {
				t.Fatalf("expected a validation error for %s", name)
			}
		})
	}
}

func TestSceneOptionsValidateAccepts(t *testing.T) {
	cases := map[string]Options{
		"minimal":              validOptions(),
		"explicit verbatim":    {Script: "s.yaml", OutDir: "out", Mode: "verbatim", ModeSet: true},
		"explicit generate":    {Script: "s.yaml", OutDir: "out", Mode: "generate", ModeSet: true},
		"zero pause":           {Script: "s.yaml", OutDir: "out", Pause: "0", PauseSet: true},
		"max-turns at the cap": {Script: "s.yaml", OutDir: "out", MaxTurns: MaxTurns, MaxTurnsSet: true},
		"mp3":                  {Script: "s.yaml", OutDir: "out", AudioFormat: "mp3"},
	}
	for name, opts := range cases {
		t.Run(name, func(t *testing.T) {
			if err := opts.Validate(); err != nil {
				t.Fatalf("Validate(%s) = %v, want no error", name, err)
			}
		})
	}
}

// An unpassed flag must not displace what the author wrote, and a passed one
// always must.
func TestSceneOptionsEffectiveOverrides(t *testing.T) {
	authored := &Document{
		Mode:  ModeGenerate,
		Pause: "250ms",
		Scene: &SceneBlock{Beats: []string{"they argue"}, MaxTurns: 8},
	}
	bare := &Document{}

	cases := []struct {
		name         string
		opts         Options
		doc          *Document
		wantMode     Mode
		wantPause    time.Duration
		wantMaxTurns int
	}{
		{"document wins when no flag was passed", validOptions(), authored, ModeGenerate, 250 * time.Millisecond, 8},
		{"defaults when neither was set", validOptions(), bare, ModeVerbatim, DefaultPause, DefaultMaxTurns},
		{"no document at all", validOptions(), nil, ModeVerbatim, DefaultPause, DefaultMaxTurns},
		{
			"flags win when passed",
			Options{Script: "s.yaml", OutDir: "out", Mode: "verbatim", ModeSet: true, Pause: "1s", PauseSet: true, MaxTurns: 12, MaxTurnsSet: true},
			authored, ModeVerbatim, time.Second, 12,
		},
		{
			"an empty flag value is not a pass",
			Options{Script: "s.yaml", OutDir: "out", Mode: "improvise", Pause: "soon", MaxTurns: 99},
			authored, ModeGenerate, 250 * time.Millisecond, 8,
		},
		{
			"an explicit zero pause means no silence",
			Options{Script: "s.yaml", OutDir: "out", Pause: "0s", PauseSet: true},
			authored, ModeGenerate, 0, 8,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.opts.EffectiveMode(tc.doc); got != tc.wantMode {
				t.Errorf("EffectiveMode = %q, want %q", got, tc.wantMode)
			}
			if got := tc.opts.EffectivePause(tc.doc); got != tc.wantPause {
				t.Errorf("EffectivePause = %s, want %s", got, tc.wantPause)
			}
			if got := tc.opts.EffectiveMaxTurns(tc.doc); got != tc.wantMaxTurns {
				t.Errorf("EffectiveMaxTurns = %d, want %d", got, tc.wantMaxTurns)
			}
		})
	}
}
