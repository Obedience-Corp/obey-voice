package pipeline

import (
	"context"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

func TestTranscriptionReturnsOfflineFinalWithoutCommittingOrRunningBrain(t *testing.T) {
	brain := &countingBrain{fakeBrain: fakeBrain{chunks: []string{"must never run"}}}
	bus := events.NewBus()
	committed, thinking := 0, 0
	events.Subscribe(bus, func(events.UserInput) { committed++ })
	events.Subscribe(bus, func(events.ThinkingStarted) { thinking++ })
	p := &Pipeline{STT: &fakeSTT{text: "hello from offline recognition"}, Brain: brain, Events: bus}
	text, err := p.RunTranscription(context.Background())
	if err != nil || text != "hello from offline recognition" {
		t.Fatalf("RunTranscription = %q, %v", text, err)
	}
	if brain.streams.Load() != 0 || committed != 0 || thinking != 0 {
		t.Fatalf("recognition dispatched a turn: brain=%d, committed=%d, thinking=%d", brain.streams.Load(), committed, thinking)
	}
}

func TestTranscriptionReportsMissingRecognizer(t *testing.T) {
	p := &Pipeline{}
	if _, err := p.RunTranscription(context.Background()); err == nil {
		t.Fatal("missing STT should report an error, not an empty successful result")
	}
}
