package pipeline

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/job"
)

// museSparkBrain is Mira's provider on Muse Spark, whose CLI fails if it is
// ever asked anything.
func museSparkBrain(t *testing.T) *brain.MuseSparkBrain {
	t.Helper()
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
	return b
}

func TestSpawnBriefGivesAWorkerOnlyAHostedPersonasShare(t *testing.T) {
	b := museSparkBrain(t)
	share := []brain.Turn{{Role: "user", Content: "Mira, plan the trip."}, {Role: "assistant", Content: "Planning it.", PersonaID: "mira", PersonaDisplayName: "Mira"}}
	canonical := append([]brain.Turn{{Role: "user", Content: "Ember, my PIN is PRIVATE-4711."}, {Role: "assistant", Content: "Noted.", PersonaID: "ember", PersonaDisplayName: "Ember"}}, share...)
	if err := b.ImportContinuity(brain.ContinuitySnapshot{Canonical: canonical, Working: share}); err != nil {
		t.Fatal(err)
	}
	a := &Agency{pipeline: &Pipeline{Brain: b}}
	brief := spawnBrief(a.liveHistory(), "Book it.", "mira")
	if !strings.Contains(brief, "Mira, plan the trip.") || !strings.Contains(brief, `persona with id "mira"`) || strings.Contains(brief, "PRIVATE-4711") || strings.Contains(strings.ToLower(brief), "ember") {
		t.Fatalf("the worker brief is not the persona's share:\n%s", brief)
	}
	if got := len(b.History()); got != len(canonical) {
		t.Fatalf("the record holds %d turns, want %d", got, len(canonical))
	}
}

func TestJobStatusNamesOnlyTheJobsAHostedPersonaMayReceive(t *testing.T) {
	type spawn struct{ persona, backend, task string }
	ember := spawn{"ember", "ollama", "read my private tax notes"}
	mira := spawn{"mira", "muse-spark", "audit the config"}
	miraLocal := spawn{"mira", "ollama", "tidy the diary"}
	miraOnClaude := spawn{"mira", "claude", "draft the memo"}
	for _, tc := range []struct {
		name   string
		hosted bool
		spawns []spawn
		want   string
	}{
		{"its own jobs are named, one started on Ollama, and the other counted", true, []spawn{ember, mira, miraLocal},
			"Still working on audit the config. So far: listed 1 folder (1 unknown). Also 1 queued. One other job is running."},
		{"only other jobs", true, []spawn{ember}, "One other job is running."},
		{"its job from another hosted backend is named", true, []spawn{miraOnClaude, mira}, "Still working on draft the memo. So far: listed 1 folder (1 unknown). Also 1 more running."},
		{"its own job queued behind others", true, []spawn{ember, ember, mira}, "Queued: audit the config. 2 other jobs are running."},
		{"on ollama every job is named", false, []spawn{ember, mira, miraLocal},
			"Still working on read my private tax notes. So far: listed 1 folder (1 unknown). Also 1 more running and 1 queued."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newBudgetHarness(t, job.WithMaxConcurrent(2))
			defer close(h.release)
			if tc.hosted {
				h.agency.pipeline.Brain = museSparkBrain(t)
			}
			for _, s := range tc.spawns {
				h.agency.ReplaceWorker(gatedHookWorker{release: h.release}, &config.Config{ActivePersona: s.persona, BrainProvider: s.backend, PersonaWork: config.WorkWorker}, nil)
				h.spawn(s.task)
			}
			for i := 0; i < min(len(tc.spawns), 2); i++ {
				select {
				case <-h.tool:
				case <-time.After(3 * time.Second):
					t.Fatalf("only %d job(s) reached a tool", i)
				}
			}
			h.agency.ReplaceWorker(gatedHookWorker{release: h.release}, &config.Config{ActivePersona: "mira", BrainProvider: "muse-spark", PersonaWork: config.WorkWorker}, nil)
			h.agency.HandleRoute(context.Background(), h.turn, h.metrics, RouteDecision{Kind: RouteJobStatus, Transcript: "status", Source: "test"})
			calls := h.tts.Calls()
			if got := calls[len(calls)-1]; got != tc.want {
				t.Fatalf("status = %q, want %q", got, tc.want)
			}
		})
	}
}
