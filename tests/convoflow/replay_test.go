package main

import (
	"context"
	"strings"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/pipeline"
)

func TestWAVReplayUsesFiniteFixtureSource(t *testing.T) {
	path := t.TempDir() + "/input.wav"
	samples := make([]float32, audio.ChunkSize+17)
	for i := range samples {
		samples[i] = 0.08
	}
	if err := audio.WriteWAVFloat32(path, audio.SampleRate, samples); err != nil {
		t.Fatal(err)
	}
	source, err := audio.NewFixtureSourceFromWAV(path, audio.ChunkSize, false)
	if err != nil {
		t.Fatalf("fixture source: %v", err)
	}
	defer source.Close()
	if source.Exhausted() {
		t.Fatal("fixture source exhausted before replay")
	}
	if got := len(source.Read()); got != audio.ChunkSize {
		t.Fatalf("first fixture chunk = %d samples, want %d", got, audio.ChunkSize)
	}
	if got := len(source.Read()); got != 17 {
		t.Fatalf("final fixture chunk = %d samples, want 17", got)
	}
	if !source.Exhausted() {
		t.Fatal("fixture source not exhausted after finite input")
	}
}

func TestWAVReplayRejectsInvalidInputsBeforeOpeningModels(t *testing.T) {
	r := &liveRunner{
		cfg:   &config.Config{},
		agent: &voiceagent.Agent{Pipeline: &pipeline.Pipeline{}},
	}
	if err := r.runWAV(context.Background(), t.TempDir()+"/missing.wav"); err == nil || !strings.Contains(err.Error(), "no such file") {
		t.Fatalf("missing WAV error = %v, want filesystem error", err)
	}
	if err := r.runWAV(context.Background(), ""); err == nil || !strings.Contains(err.Error(), "path is empty") {
		t.Fatalf("empty path error = %v", err)
	}
}
