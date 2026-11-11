package pipeline

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

func TestSpawnBriefGivesAWorkerOnlyAHostedPersonasShare(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX fixture CLI")
	}
	binDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(binDir, "muse"), []byte("#!/bin/sh\nexit 1\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	b, err := brain.NewMuseSpark(&config.Config{BrainProvider: "muse-spark", AgentName: "Mira", MaxHistory: 20, PromptsDir: t.TempDir(), WorkDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	share := []brain.Turn{{Role: "user", Content: "Mira, plan the trip."}, {Role: "assistant", Content: "Planning it.", PersonaID: "mira", PersonaDisplayName: "Mira"}}
	canonical := append([]brain.Turn{{Role: "user", Content: "Ember, my PIN is PRIVATE-4711."}, {Role: "assistant", Content: "Noted.", PersonaID: "ember", PersonaDisplayName: "Ember"}}, share...)
	if err := b.ImportContinuity(brain.ContinuitySnapshot{Canonical: canonical, Working: share}); err != nil {
		t.Fatal(err)
	}
	a := &Agency{pipeline: &Pipeline{Brain: b}}
	brief := spawnBrief(a.liveHistory(), "Book it.")
	if !strings.Contains(brief, "Mira, plan the trip.") || strings.Contains(brief, "PRIVATE-4711") || strings.Contains(brief, "Ember") {
		t.Fatalf("the worker brief is not the persona's share:\n%s", brief)
	}
	if got := len(b.History()); got != len(canonical) {
		t.Fatalf("the record holds %d turns, want %d", got, len(canonical))
	}
}
