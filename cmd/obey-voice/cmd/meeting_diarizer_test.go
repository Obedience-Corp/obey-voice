//go:build !integration

package cmd

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Obedience-Corp/obey-voice/internal/meeting"
	"github.com/Obedience-Corp/obey-voice/internal/meeting/remote"
	"github.com/Obedience-Corp/obey-voice/internal/speaker"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

func TestMeetingDiarizerSelection(t *testing.T) {
	tests := []struct {
		name     string
		diarizer string
		wantErr  string
	}{
		{
			name:     "unknown engine is refused",
			diarizer: "pyannote",
			wantErr:  "unsupported speaker.meeting.diarizer",
		},
		{
			// The CLI is not installed here, and that is the point: selecting
			// Sortformer has to fail at meeting start, not at finalize.
			name:     "sortformer without the CLI fails at start",
			diarizer: config.DiarizerSortformer,
			wantErr:  "transcribe.cpp",
		},
		{name: "unset stays on sherpa", diarizer: ""},
		{name: "sherpa", diarizer: config.DiarizerSherpa},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.Config{ModelsDir: t.TempDir(), TranscribeCPPBinary: "obey-voice-absent-transcribe-cli"}
			cfg.Speaker.Meeting.Diarizer = tt.diarizer

			diarizer, err := meetingDiarizer(context.Background(), cfg)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("meetingDiarizer() error = %v, want it to mention %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("meetingDiarizer() error = %v", err)
			}
			// nil means the analyzer keeps the engine's own review path: the
			// default must never reach for an external CLI.
			if diarizer != nil {
				t.Fatalf("meetingDiarizer() = %T, want the default sherpa path (nil)", diarizer)
			}
		})
	}
}

// meetingAnalyzer is the one seam every review path shares. Testing it with a
// fake engine covers `meeting record`, `meeting analyze` and the serve
// finish-a-bundle pipeline at once — the serve path was wired late precisely
// because each site used to build its own analyzer.
func TestMeetingAnalyzerAppliesTheSelection(t *testing.T) {
	sp := speaker.Config{Enabled: true}
	sp.Meeting.Enabled = true

	t.Run("default keeps the engine's own review path", func(t *testing.T) {
		cfg := &config.Config{ModelsDir: t.TempDir()}
		engine := &speaker.FakeEngine{}
		analyzer, err := meetingAnalyzer(context.Background(), cfg, sp, engine)
		if err != nil {
			t.Fatalf("meetingAnalyzer() error = %v", err)
		}
		defer func() { _ = analyzer.Close() }()

		// The fake engine answers Diarize, so reaching observations proves the
		// review ran on the engine rather than on an external CLI.
		timeline, err := analyzer.Finalize(context.Background(), make([]float32, 16000))
		if err != nil {
			t.Fatalf("Finalize() error = %v", err)
		}
		if len(timeline.Observations) == 0 {
			t.Fatal("no observations; the default sherpa review path did not run")
		}
	})

	t.Run("sortformer without its CLI refuses to build an analyzer", func(t *testing.T) {
		cfg := &config.Config{ModelsDir: t.TempDir(), TranscribeCPPBinary: "obey-voice-absent-transcribe-cli"}
		cfg.Speaker.Meeting.Diarizer = config.DiarizerSortformer
		spSort := sp
		spSort.Meeting.Diarizer = config.DiarizerSortformer

		analyzer, err := meetingAnalyzer(context.Background(), cfg, spSort, &speaker.FakeEngine{})
		if err == nil {
			_ = analyzer.Close()
			t.Fatal("meetingAnalyzer() error = nil; a selected diarizer that cannot run must not fall back to sherpa")
		}
		if !strings.Contains(err.Error(), "transcribe.cpp") {
			t.Fatalf("error = %q, want it to name the missing CLI", err)
		}
	})

	t.Run("unknown engine refuses to build an analyzer", func(t *testing.T) {
		cfg := &config.Config{ModelsDir: t.TempDir()}
		cfg.Speaker.Meeting.Diarizer = "pyannote"
		if _, err := meetingAnalyzer(context.Background(), cfg, sp, &speaker.FakeEngine{}); err == nil ||
			!strings.Contains(err.Error(), "unsupported speaker.meeting.diarizer") {
			t.Fatalf("error = %v, want the unknown engine refused", err)
		}
	})
}

// The serve pipeline reads the key too. A meeting recorded on a phone and
// finished through `serve` must not silently review with sherpa.
func TestDiarizeMeetingAudioConsultsTheDiarizerKey(t *testing.T) {
	job := remote.Job{BundlePath: filepath.Join(t.TempDir(), "planning.meeting")}

	t.Run("meeting analysis off short-circuits", func(t *testing.T) {
		cfg := &config.Config{ModelsDir: t.TempDir()}
		result, err := diarizeMeetingAudio(context.Background(), cfg, job)
		if err != nil {
			t.Fatalf("diarizeMeetingAudio() error = %v", err)
		}
		if result.Status != meeting.AnalysisDisabled {
			t.Fatalf("status = %s, want disabled", result.Status)
		}
	})

	t.Run("unknown engine fails the job instead of falling back", func(t *testing.T) {
		cfg := &config.Config{ModelsDir: t.TempDir()}
		cfg.Speaker.Enabled = true
		cfg.Speaker.Meeting.Enabled = true
		cfg.Speaker.Meeting.Diarizer = "pyannote"

		// Fails while resolving the asset plan, before any download or model
		// load, so this stays a pure unit test.
		if _, err := diarizeMeetingAudio(context.Background(), cfg, job); err == nil ||
			!strings.Contains(err.Error(), "unsupported speaker.meeting.diarizer") {
			t.Fatalf("error = %v, want the serve path to refuse an unusable diarizer", err)
		}
	})
}

// The defect this PR was sent back for: one review path built its analyzer
// directly and so ignored speaker.meeting.diarizer. Every meeting analyzer must
// come from meetingAnalyzer, which is the only place the selection is applied.
func TestNoMeetingPathBuildsItsOwnAnalyzer(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		source, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(source), "speaker.NewAnalyzer(") {
			t.Errorf("%s constructs an analyzer directly; use meetingAnalyzer so speaker.meeting.diarizer applies", name)
		}
	}
}

// Native sherpa graphs must not load at meeting start: live labels already
// own TitaNet, and review only needs pyannote + the second extractor after
// stop. Sortformer still fails in prepareMeetingSpeakers via meetingDiarizer.
func TestPrepareMeetingSpeakersDefersSherpaEngine(t *testing.T) {
	source, err := os.ReadFile("meeting_runtime.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	const marker = "func prepareMeetingSpeakers"
	idx := strings.Index(text, marker)
	if idx < 0 {
		t.Fatal("prepareMeetingSpeakers not found")
	}
	fn := text[idx:]
	if next := strings.Index(fn[len(marker):], "\nfunc "); next >= 0 {
		fn = fn[:len(marker)+next]
	}
	if !strings.Contains(fn, "meeting.NewDeferredAnalyzer") {
		t.Fatal("prepareMeetingSpeakers must defer native diarizer load with NewDeferredAnalyzer")
	}
	if !strings.Contains(fn, "meetingDiarizer(") {
		t.Fatal("prepareMeetingSpeakers must still fail-fast sortformer/unknown engines at start")
	}
	load := strings.Index(fn, "speaker.NewSherpaEngine")
	deferIdx := strings.Index(fn, "meeting.NewDeferredAnalyzer")
	if load < 0 || load < deferIdx {
		t.Fatal("NewSherpaEngine must run inside the deferred builder, not at meeting start")
	}
}

// Deferring the engine must not defer the model-path check with it: a typo in
// speaker.models.segmentation removes the managed asset from the manifest, so
// EnsureRuntimeAssets sees nothing to verify and nothing else looks at the path.
// Custom paths keep the manifest empty, so this test never downloads.
func TestPrepareMeetingSpeakersRejectsMissingModelPathAtStart(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{ModelsDir: dir}
	cfg.Speaker.Enabled = true
	cfg.Speaker.Meeting.Enabled = true
	cfg.Speaker.Models.Segmentation = filepath.Join(dir, "missing-seg.onnx")
	cfg.Speaker.Models.Embedding = filepath.Join(dir, "missing-emb.onnx")

	session, err := prepareMeetingSpeakers(
		context.Background(), cfg, audio.NewCapture(), nil, filepath.Join(dir, "planning.meeting"), nil,
	)
	if err == nil {
		if session != nil {
			_ = session.Close()
		}
		t.Fatal("prepareMeetingSpeakers() error = nil; a missing model path must fail before the recording starts")
	}
	if !strings.Contains(err.Error(), cfg.Speaker.Models.Segmentation) {
		t.Fatalf("error = %q, want it to name the missing segmentation model %q", err, cfg.Speaker.Models.Segmentation)
	}
}

// The other half of the same guarantee: paths that resolve are accepted at
// start without loading anything. These files are empty, so a session comes
// back only because the native graphs were not opened.
func TestPrepareMeetingSpeakersStartsWithoutLoadingModels(t *testing.T) {
	dir := t.TempDir()
	seg := filepath.Join(dir, "seg.onnx")
	emb := filepath.Join(dir, "emb.onnx")
	for _, path := range []string{seg, emb} {
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cfg := &config.Config{ModelsDir: dir}
	cfg.Speaker.Enabled = true
	cfg.Speaker.Meeting.Enabled = true
	cfg.Speaker.Models.Segmentation = seg
	cfg.Speaker.Models.Embedding = emb

	session, err := prepareMeetingSpeakers(
		context.Background(), cfg, audio.NewCapture(), nil, filepath.Join(dir, "planning.meeting"), nil,
	)
	if err != nil {
		t.Fatalf("prepareMeetingSpeakers() error = %v", err)
	}
	if session == nil {
		t.Fatal("prepareMeetingSpeakers() session = nil with meeting analysis enabled")
	}
	if err := session.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
}
