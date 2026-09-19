//go:build !integration

package cmd

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/Obedience-Corp/obey-voice/internal/speaker"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

// TestLiveSpeakerFirstEnableEnsuresManagedEmbedding covers a fresh install:
// startup skipped speaker assets because config was off, then /speakers on must
// ensure TitaNet before the lazy engine constructor tries to open it.
func TestLiveSpeakerFirstEnableEnsuresManagedEmbedding(t *testing.T) {
	cfg := &config.Config{ModelsDir: t.TempDir()}
	sp := speaker.FromAppConfig(cfg)
	modelPath := speaker.ResolveSherpaModelPaths(sp, cfg.ModelsDir).Embedding

	ensure := func(ctx context.Context, got *config.Config, req config.AssetRequest, _ func(string, float64)) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if got == cfg {
			t.Fatal("asset ensure mutated the session config instead of a copy")
		}
		if !got.Speaker.Enabled || !got.Speaker.Live.Enabled {
			t.Fatalf("speaker asset config = %+v, want master and live enabled", got.Speaker)
		}
		if req != (config.AssetRequest{NeedSpeaker: true}) {
			t.Fatalf("asset request = %+v, want speaker only", req)
		}
		manifest, err := config.ManifestFor(got, req)
		if err != nil {
			return err
		}
		if len(manifest.Assets) != 1 || manifest.Assets[0].Kind != config.AssetKindSpeaker {
			t.Fatalf("first-enable manifest = %+v, want only the managed speaker embedding", manifest.Assets)
		}
		if _, err := os.Stat(modelPath); !os.IsNotExist(err) {
			t.Fatalf("model should begin absent, stat error = %v", err)
		}
		if err := os.MkdirAll(filepath.Dir(modelPath), 0o755); err != nil {
			return err
		}
		return os.WriteFile(modelPath, []byte("managed-titanet"), 0o644)
	}

	build := liveSpeakerEngineBuilder(t.Context(), cfg, ensure,
		func() (speaker.Engine, error) {
			if _, err := os.Stat(modelPath); err != nil {
				return nil, err
			}
			return &speaker.FakeEngine{}, nil
		}, func() (speaker.Engine, error) {
			t.Fatal("full engine fallback called after live engine succeeded")
			return nil, nil
		})

	engine, err := build()
	if err != nil {
		t.Fatalf("first-enable build error = %v", err)
	}
	defer func() { _ = engine.Close() }()
}

// Meeting diarization is on by default; the live ensure must still ask for
// TitaNet only.
func TestLiveSpeakerEngineBuilderDoesNotPullMeetingSegmentation(t *testing.T) {
	cfg := &config.Config{ModelsDir: t.TempDir()}
	cfg.Speaker.Enabled = true
	cfg.Speaker.Live.Enabled = true
	cfg.Speaker.Meeting.Enabled = true

	ensure := func(_ context.Context, got *config.Config, req config.AssetRequest, _ func(string, float64)) error {
		if got.Speaker.Meeting.Enabled {
			t.Fatal("live ensure copied speaker.meeting.enabled; pyannote must stay off this path")
		}
		if req != (config.AssetRequest{NeedSpeaker: true}) {
			t.Fatalf("asset request = %+v, want speaker only", req)
		}
		manifest, err := config.ManifestFor(got, req)
		if err != nil {
			return err
		}
		if len(manifest.Assets) != 1 || manifest.Assets[0].ID != "speaker.embedding.nemo-titanet-small" {
			t.Fatalf("live manifest = %+v, want only TitaNet", manifest.Assets)
		}
		return nil
	}

	build := liveSpeakerEngineBuilder(t.Context(), cfg, ensure,
		func() (speaker.Engine, error) { return &speaker.FakeEngine{}, nil },
		func() (speaker.Engine, error) {
			t.Fatal("full engine fallback called after live engine succeeded")
			return nil, nil
		})
	engine, err := build()
	if err != nil {
		t.Fatalf("build error = %v", err)
	}
	defer func() { _ = engine.Close() }()
}

func TestPrepareLiveSpeakerDoesNotEnableOnBuild(t *testing.T) {
	cfg := &config.Config{ModelsDir: t.TempDir()}
	cfg.Speaker.Enabled = true
	cfg.Speaker.Live.Enabled = true

	controller, stop, detail := prepareLiveSpeaker(t.Context(), cfg, audio.NewCapture())
	defer stop()
	if detail == "" {
		t.Fatal("detail is empty, want the configured-on path")
	}
	if got := controller.Stats().Status; got != speaker.LiveDisabled {
		t.Fatalf("status after build = %q, want %q until the session screen enables it", got, speaker.LiveDisabled)
	}
}
