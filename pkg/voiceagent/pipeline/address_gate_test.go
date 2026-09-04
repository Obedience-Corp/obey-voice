package pipeline

import (
	"context"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

// The name gate drops an unaddressed utterance the way the speaker gate drops
// an unverified one: quietly, before the brain, and without an error.
func TestRunTurnAddressGateVetoesTurn(t *testing.T) {
	brainProvider := &countingBrain{fakeBrain: fakeBrain{chunks: []string{"must never run"}}}
	addressed := false
	var infos []string
	bus := events.NewBus()
	events.Subscribe(bus, func(e events.Info) { infos = append(infos, e.Message) })

	p := &Pipeline{
		STT:     &fakeSTT{text: "what's for dinner"},
		Brain:   brainProvider,
		VAD:     &fakeVAD{},
		Capture: newFakeCapture(),
		Events:  bus,
		AddressGate: func(string) string {
			if !addressed {
				return "Not addressed to Obey — ignored"
			}
			return ""
		},
	}

	text, err := p.RunTurn(context.Background())
	if err != nil {
		t.Fatalf("RunTurn() error = %v", err)
	}
	if text != "" {
		t.Fatalf("vetoed turn returned text %q", text)
	}
	if n := brainProvider.streams.Load(); n != 0 {
		t.Fatalf("brain called %d time(s) on a turn nobody addressed", n)
	}
	if len(infos) != 1 || infos[0] != "Not addressed to Obey — ignored" {
		t.Fatalf("Info events = %v, want the gate's reason once", infos)
	}

	addressed = true
	text, err = p.RunTurn(context.Background())
	if err != nil {
		t.Fatalf("RunTurn() error = %v", err)
	}
	if text == "" {
		t.Fatal("addressed turn returned no text")
	}
	if n := brainProvider.streams.Load(); n != 1 {
		t.Fatalf("brain calls = %d, want 1", n)
	}
}

// A nil gate is the default and every existing session: turns run untouched.
func TestRunTurnWithoutAddressGate(t *testing.T) {
	brainProvider := &countingBrain{fakeBrain: fakeBrain{chunks: []string{"sure"}}}
	p := &Pipeline{
		STT:     &fakeSTT{text: "what's for dinner"},
		Brain:   brainProvider,
		VAD:     &fakeVAD{},
		Capture: newFakeCapture(),
		Events:  events.NewBus(),
	}

	text, err := p.RunTurn(context.Background())
	if err != nil {
		t.Fatalf("RunTurn() error = %v", err)
	}
	if text == "" {
		t.Fatal("ungated turn returned no text")
	}
	if n := brainProvider.streams.Load(); n != 1 {
		t.Fatalf("brain calls = %d, want 1", n)
	}
}

// The name gate runs first, so an utterance nobody addressed never reaches the
// speaker gate — there is no identity question to answer about it.
func TestAddressGateRunsBeforeSpeakerGate(t *testing.T) {
	speakerChecks := 0
	p := &Pipeline{
		STT:         &fakeSTT{text: "what's for dinner"},
		Brain:       &countingBrain{fakeBrain: fakeBrain{chunks: []string{"never"}}},
		VAD:         &fakeVAD{},
		Capture:     newFakeCapture(),
		Events:      events.NewBus(),
		AddressGate: func(string) string { return "not addressed" },
		SpeakerGate: func(string) string { speakerChecks++; return "" },
	}

	if _, err := p.RunTurn(context.Background()); err != nil {
		t.Fatalf("RunTurn() error = %v", err)
	}
	if speakerChecks != 0 {
		t.Fatalf("speaker gate consulted %d time(s) on an unaddressed turn", speakerChecks)
	}
}
