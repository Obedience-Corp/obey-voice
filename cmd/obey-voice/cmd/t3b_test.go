//go:build !integration

package cmd

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	meetinglog "github.com/Obedience-Corp/obey-voice/internal/meeting/log"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/stt"
)

// T3b step 1: replay the meeting fixture through the shipped WAV->transcript
// path (newReplaySource + replayTranscribe + meetinglog.Writer) and dump the
// resulting TranscriptRecords, which is exactly what AttributeTranscript is
// fed in production (internal/meeting.logAnalysis).
func TestT3bTranscribeFixture(t *testing.T) {
	fixture := os.Getenv("T3B_FIXTURE")
	out := os.Getenv("T3B_OUT")
	if fixture == "" || out == "" {
		t.Skip("set T3B_FIXTURE and T3B_OUT")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()

	loaded, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	cfg := *loaded
	cfg.VADEnabled = true
	if err := config.EnsureRuntimeAssets(ctx, &cfg, config.AssetRequest{NeedSTT: true, NeedVAD: true}, nil); err != nil {
		t.Fatal(err)
	}
	t.Logf("stt provider=%s mode=%s", cfg.STTProvider, cfg.STTMode)

	source, err := newReplaySource(fixture)
	if err != nil {
		t.Fatal(err)
	}
	vad, err := audio.NewVAD(&cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer vad.Delete()

	provider, cleanup, err := stt.NewProvider(&cfg, source, vad)
	if err != nil {
		t.Fatal(err)
	}
	if cleanup != nil {
		defer cleanup()
	}

	dir := t.TempDir()
	bundle := filepath.Join(dir, "t3b.meeting")
	writer, err := meetinglog.CreateBundle(bundle, "T3b attribution probe", cfg.STTProvider, "fixture")
	if err != nil {
		t.Fatal(err)
	}
	if err := replayTranscribe(ctx, source, provider, writer); err != nil {
		t.Fatal(err)
	}
	records := writer.Transcripts()
	if _, err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if len(records) == 0 {
		t.Fatal("no transcript records produced")
	}
	blob, err := json.MarshalIndent(struct {
		Fixture     string                        `json:"fixture"`
		Provider    string                        `json:"stt_provider"`
		Mode        string                        `json:"stt_mode"`
		Transcripts []meetinglog.TranscriptRecord `json:"transcripts"`
	}{fixture, cfg.STTProvider, cfg.STTMode, records}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(out, append(blob, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Logf("wrote %d transcript records to %s", len(records), out)
}
