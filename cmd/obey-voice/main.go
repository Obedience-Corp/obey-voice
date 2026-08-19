package main

import (
	"os"
	"strings"

	// Seed lipgloss dark-background cache before bubbletea OSC queries.
	_ "github.com/Obedience-Corp/obey-voice/internal/bginit"
	// Register persona ensure/apply on config.Load.
	_ "github.com/Obedience-Corp/obey-voice/internal/persona"

	"github.com/Obedience-Corp/obey-voice/cmd/obey-voice/cmd"
)

// adoptLegacyEnv mirrors SAMANTHA_* variables onto their OBEY_VOICE_* names
// for one release cycle so shells, LaunchAgents, and scripts written before
// the rename keep working. An explicit OBEY_VOICE_* value always wins.
func adoptLegacyEnv() {
	const legacy, current = "SAMANTHA_", "OBEY_VOICE_"
	for _, kv := range os.Environ() {
		name, value, ok := strings.Cut(kv, "=")
		if !ok || !strings.HasPrefix(name, legacy) {
			continue
		}
		if _, set := os.LookupEnv(current + name[len(legacy):]); !set {
			_ = os.Setenv(current+name[len(legacy):], value)
		}
	}
}

func main() {
	adoptLegacyEnv()
	// fang renders styled errors itself, so only the exit status is decided
	// here. Commands that distinguish "called wrong" from "operation failed"
	// (the config surface) carry their own status; everything else exits 1.
	if err := cmd.Execute(); err != nil {
		os.Exit(cmd.ExitCode(err))
	}
}
