package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/pipeline"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/stt"
)

type replayVAD struct{}

func (*replayVAD) AcceptWaveform([]float32) {}
func (*replayVAD) IsSpeech() bool           { return true }
func (*replayVAD) IsSpeechDetected() bool   { return true }
func (*replayVAD) Clear()                   {}

type replaySTT struct{ text string }
type replaySTTSession struct{ events chan stt.Event }

func (s *replaySTTSession) Events() <-chan stt.Event { return s.events }
func (s *replaySTTSession) Close() error             { return nil }
func (s *replaySTT) Available() bool                 { return true }
func (s *replaySTT) Start(context.Context) (stt.Session, error) {
	e := make(chan stt.Event, 1)
	if s.text == "" {
		e <- stt.Timeout{}
	} else {
		e <- stt.FinalTranscript{Text: s.text}
	}
	close(e)
	return &replaySTTSession{events: e}, nil
}

type replayBrain struct{}

func (*replayBrain) ThinkStream(ctx context.Context, _ string, _ brain.StreamOptions) (*brain.Stream, error) {
	chunks, done := make(chan string, 1), make(chan brain.StreamResult, 1)
	chunks <- "fixture reply"
	close(chunks)
	done <- brain.StreamResult{}
	close(done)
	return &brain.Stream{Chunks: chunks, Done: done}, nil
}
func (*replayBrain) ThinkFull(context.Context, string, brain.StreamOptions) (string, error) {
	return "fixture reply", nil
}
func (*replayBrain) ClearHistory()            {}
func (*replayBrain) History() []brain.Turn    { return nil }
func (*replayBrain) LoadHistory([]brain.Turn) {}

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

func TestWAVReplayRunsSamePipelineAndRestoresProviders(t *testing.T) {
	path := t.TempDir() + "/speech.wav"
	if err := audio.WriteWAVFloat32(path, audio.SampleRate, []float32{0.8, 0.8, 0.8}); err != nil {
		t.Fatal(err)
	}
	originalSTT, originalVAD := &replaySTT{text: "original"}, &replayVAD{}
	p := &pipeline.Pipeline{STT: originalSTT, VAD: originalVAD, Brain: &replayBrain{}, Events: events.NewBus()}
	r := &liveRunner{cfg: &config.Config{}, agent: &voiceagent.Agent{Pipeline: p}}
	r.wavVADFactory = func(*config.Config) (pipeline.VoiceDetector, func(), error) { return &replayVAD{}, func() {}, nil }
	var factorySource *audio.FixtureSource
	r.wavSTTFactory = func(_ *config.Config, source *audio.FixtureSource, _ pipeline.VoiceDetector) (stt.Provider, func(), error) {
		factorySource = source
		return &replaySTT{text: "recorded question"}, func() {}, nil
	}
	if err := r.runWAV(context.Background(), path); err != nil {
		t.Fatal(err)
	}
	if factorySource == nil {
		t.Fatal("WAV fixture source was not passed through the production seam")
	}
	if p.STT != originalSTT || p.VAD != originalVAD {
		t.Fatal("runWAV did not restore pipeline providers")
	}

	r.wavSTTFactory = func(*config.Config, *audio.FixtureSource, pipeline.VoiceDetector) (stt.Provider, func(), error) {
		return &replaySTT{}, func() {}, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := r.runWAV(ctx, path); err == nil || !strings.Contains(err.Error(), "no speech transcript") {
		t.Fatalf("empty transcript error = %v", err)
	}
}
