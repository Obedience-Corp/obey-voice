//go:build speakerflow
// +build speakerflow

package speakerflow

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Obedience-Corp/obey-voice/internal/speaker"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

const (
	zoomFixtureName    = "zoom-call.wav"
	zoomAccuracyTarget = 0.95
)

type zoomRoster struct {
	SourceURL   string `json:"source_url"`
	FixtureFile string `json:"fixture_file"`
	LabeledRegion struct {
		StartMS int64  `json:"start_ms"`
		EndMS   int64  `json:"end_ms"`
		Note    string `json:"note"`
	} `json:"labeled_region_ms"`
	Speakers   []string          `json:"speakers"`
	Enrollment []zoomLabeledSpan `json:"enrollment"`
	Utterances []zoomLabeledSpan `json:"utterances"`
}

type zoomLabeledSpan struct {
	Speaker string `json:"speaker"`
	StartMS int64  `json:"start_ms"`
	EndMS   int64  `json:"end_ms"`
}

func zoomFixtureCandidates() []string {
	var paths []string
	if p := os.Getenv("OBEY_VOICE_ZOOM_FIXTURE"); p != "" {
		paths = append(paths, p)
	}
	cacheDir := os.Getenv("OBEY_VOICE_FIXTURE_CACHE")
	if cacheDir == "" {
		if xdg := os.Getenv("XDG_CACHE_HOME"); xdg != "" {
			cacheDir = filepath.Join(xdg, config.AppSlug, "fixtures", "meetings")
		} else if home, err := os.UserHomeDir(); err == nil {
			cacheDir = filepath.Join(home, ".cache", config.AppSlug, "fixtures", "meetings")
		}
	}
	if cacheDir != "" {
		paths = append(paths, filepath.Join(cacheDir, zoomFixtureName))
	}
	dir, err := os.Getwd()
	if err == nil {
		for i := 0; i < 6; i++ {
			paths = append(paths, filepath.Join(dir, "tests", "fixtures", "meetings", zoomFixtureName))
			parent := filepath.Dir(dir)
			if parent == dir {
				break
			}
			dir = parent
		}
	}
	return paths
}

func zoomFixturePath(t *testing.T) string {
	t.Helper()
	for _, candidate := range zoomFixtureCandidates() {
		if st, err := os.Stat(candidate); err == nil && st.Size() > 100_000 {
			t.Logf("using zoom fixture %s (%.1f MiB)", candidate, float64(st.Size())/(1<<20))
			return candidate
		}
	}
	t.Skipf("zoom fixture missing — run: just fetch-zoom-fixture\n  looked in:\n  - %s",
		joinLines(zoomFixtureCandidates()))
	return ""
}

func loadZoomRoster(t *testing.T) zoomRoster {
	t.Helper()
	path := filepath.Join("tests", "speakerflow", "zoom-call-roster.json")
	if _, err := os.Stat(path); err != nil {
		path = "zoom-call-roster.json"
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read roster %s: %v", path, err)
	}
	var roster zoomRoster
	if err := json.Unmarshal(raw, &roster); err != nil {
		t.Fatalf("parse roster: %v", err)
	}
	return roster
}

func sliceSamples(samples []float32, startMS, endMS int64) []float32 {
	start := int(startMS) * fixtureRate / 1000
	end := int(endMS) * fixtureRate / 1000
	if start < 0 {
		start = 0
	}
	if end > len(samples) {
		end = len(samples)
	}
	if start >= end {
		return nil
	}
	out := make([]float32, end-start)
	copy(out, samples[start:end])
	return out
}

func newLiveEngine(t *testing.T) *speaker.SherpaEngine {
	t.Helper()
	appCfg := &config.Config{ModelsDir: config.ModelsDir()}
	appCfg.Speaker.Enabled = true
	appCfg.Speaker.Live.Enabled = true
	if err := config.EnsureRuntimeAssets(t.Context(), appCfg, config.AssetRequest{NeedSpeaker: true}, nil); err != nil {
		t.Fatalf("EnsureRuntimeAssets: %v", err)
	}
	cfg := speaker.FromAppConfig(appCfg)
	engine, err := speaker.NewSherpaLiveEngine(cfg, config.ModelsDirFrom(appCfg))
	if err != nil {
		t.Fatalf("NewSherpaLiveEngine: %v", err)
	}
	t.Cleanup(func() { _ = engine.Close() })
	return engine
}

func writeAccuracyReport(t *testing.T, correct, total int, rate float64, note string) {
	t.Helper()
	if total == 0 {
		return
	}
	var b strings.Builder
	if note != "" {
		b.WriteString(note)
		b.WriteString("\n\n")
	}
	fmt.Fprintf(&b, "## Zoom named-label accuracy\n\n")
	fmt.Fprintf(&b, "- Metric: correct attributions / labeled roster utterances\n")
	fmt.Fprintf(&b, "- Result: %d/%d = %.1f%%\n", correct, total, rate*100)
	fmt.Fprintf(&b, "- Target: 95%% (intent unchanged)\n")
	if rate < zoomAccuracyTarget {
		b.WriteString("\n### Next engine change\n\n")
		b.WriteString("- Refine roster enrollment windows against the cached clip.\n")
		b.WriteString("- Measure FRR before lowering Search threshold.\n")
		b.WriteString("- Do not re-enable Register-on-miss in Identify.\n")
	}
	candidates := []string{
		filepath.Join("festivals", "planning", "obey-voice-intents-OV0001", "003_IMPLEMENT", "02_speaker_identification", "results", "accuracy.md"),
		filepath.Join("..", "..", "..", "festivals", "planning", "obey-voice-intents-OV0001", "003_IMPLEMENT", "02_speaker_identification", "results", "accuracy.md"),
	}
	for _, path := range candidates {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			continue
		}
		if err := os.WriteFile(path, []byte(b.String()), 0o644); err == nil {
			t.Logf("wrote %s", path)
			return
		}
	}
	t.Logf("accuracy report:\n%s", b.String())
}
