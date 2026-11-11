package pipeline

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

// streamClock records when the brain was asked to answer and what it was sent.
type streamClock struct {
	compactBrain
	mu     sync.Mutex
	at     []time.Time
	inputs []string
}

func (b *streamClock) ThinkStream(_ context.Context, input string, _ brain.StreamOptions) (*brain.Stream, error) {
	b.mu.Lock()
	b.at = append(b.at, time.Now())
	b.inputs = append(b.inputs, input)
	b.mu.Unlock()
	out := make(chan string, 1)
	done := make(chan brain.StreamResult, 1)
	out <- "ok"
	close(out)
	done <- brain.StreamResult{}
	close(done)
	return &brain.Stream{Chunks: out, Done: done}, nil
}

func TestHostedNoticeIsShownAndSpokenBeforeTheRequestAndNeverSent(t *testing.T) {
	const notice = "Mira runs on Claude, a hosted service."
	b := &streamClock{}
	tts := &fakeTTS{}
	player := newFakePlayer(time.Millisecond)
	defer player.Close()
	bus := events.NewBus()
	var infos []string
	events.Subscribe(bus, func(e events.Info) { infos = append(infos, e.Message) })
	p := &Pipeline{Brain: b, TTS: tts, Player: player, Events: bus}
	p.AdmitInput = func(_ context.Context, input string, _ bool) (ResponseAdmission, error) {
		return ResponseAdmission{Request: input, Envelope: events.ResponseEnvelope{ResponseID: "r1", PersonaID: "mira", Hosted: true}}, nil
	}
	asked := 0
	p.HostedNotice = func(events.ResponseEnvelope) (HostedNotice, bool) {
		asked++
		return HostedNotice{Text: notice, Spoken: asked == 1}, asked <= 2
	}

	if err := p.RunTurnTextMode(t.Context(), "hello"); err != nil {
		t.Fatal(err)
	}
	if len(infos) != 1 || infos[0] != notice {
		t.Fatalf("info lines = %q", infos)
	}
	spoken := tts.Texts()
	if len(spoken) < 1 || spoken[0] != notice {
		t.Fatalf("spoken = %q, want the notice first", spoken)
	}
	tts.mu.Lock()
	first := tts.callTimes[0]
	tts.mu.Unlock()
	if len(b.at) != 1 || !first.Before(b.at[0]) {
		t.Fatalf("the notice was not spoken before the request (stream calls %d)", len(b.at))
	}
	for _, input := range b.inputs {
		if strings.Contains(input, "hosted service") {
			t.Fatalf("the notice reached the model: %q", input)
		}
	}

	if err := p.RunTurnTextMode(t.Context(), "again"); err != nil {
		t.Fatal(err)
	}
	if len(infos) != 2 {
		t.Fatalf("a second notice was shown with its own info line only: %q", infos)
	}
	if got := tts.Texts(); strings.Count(strings.Join(got, "|"), notice) != 1 {
		t.Fatalf("a notice marked as unspoken was spoken: %q", got)
	}
}

func TestHostedNoticeStoppedTurnSendsNothing(t *testing.T) {
	b := &streamClock{}
	p := &Pipeline{Brain: b, Events: events.NewBus()}
	ctx, cancel := context.WithCancel(t.Context())
	p.AdmitInput = func(_ context.Context, input string, _ bool) (ResponseAdmission, error) {
		return ResponseAdmission{Request: input, Envelope: events.ResponseEnvelope{ResponseID: "r1", PersonaID: "mira", Hosted: true}}, nil
	}
	p.HostedNotice = func(events.ResponseEnvelope) (HostedNotice, bool) {
		cancel()
		return HostedNotice{Text: "Mira runs on Claude, a hosted service."}, true
	}
	if err := p.RunTurnTextMode(ctx, "hello"); err == nil {
		t.Fatal("a stopped turn reported success")
	}
	if len(b.at) != 0 {
		t.Fatalf("the request was sent after the turn was stopped (%d)", len(b.at))
	}
}
