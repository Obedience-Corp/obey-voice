package pipeline

import (
	"context"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/stt"
)

// valueSession deliberately contains a slice. Comparing two stt.Session
// interfaces holding this value panics, so capture lifecycle code must use its
// generation fence instead of comparing session values.
type valueSession struct {
	events  chan stt.Event
	payload []byte
}

func (s valueSession) Events() <-chan stt.Event { return s.events }
func (valueSession) Close() error               { return nil }

type valueSessionProvider struct {
	session valueSession
}

func (p valueSessionProvider) Start(context.Context) (stt.Session, error) {
	return p.session, nil
}

func (valueSessionProvider) Available() bool { return true }

func TestRunTurnAcceptsSliceContainingValueSession(t *testing.T) {
	session := valueSession{
		events:  make(chan stt.Event, 1),
		payload: []byte{1, 2, 3},
	}
	session.events <- stt.FinalTranscript{Text: "hello"}
	p := &Pipeline{
		STT:   valueSessionProvider{session: session},
		Brain: &fakeBrain{chunks: []string{"ack"}},
		TTS:   &fakeTTS{},
	}

	text, err := p.RunTurn(context.Background())
	if err != nil {
		t.Fatalf("RunTurn() error = %v", err)
	}
	if text != "hello" {
		t.Fatalf("RunTurn() text = %q, want hello", text)
	}
}

func TestStaleCaptureGenerationCannotClearCurrentSession(t *testing.T) {
	current := valueSession{events: make(chan stt.Event), payload: []byte{2}}
	stale := valueSession{events: make(chan stt.Event), payload: []byte{1}}
	p := &Pipeline{
		captureSession:    current,
		captureGeneration: 2,
		captureYielded:    true,
	}

	p.finishCaptureSession(stale, 1, false)

	p.captureMu.Lock()
	defer p.captureMu.Unlock()
	if p.captureSession == nil {
		t.Fatal("stale capture cleanup cleared the current session")
	}
	if p.captureGeneration != 2 {
		t.Fatalf("capture generation = %d, want 2", p.captureGeneration)
	}
	if !p.captureYielded {
		t.Fatal("stale capture cleanup changed current yielded state")
	}
}
