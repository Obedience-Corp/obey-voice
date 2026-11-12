package pipeline

import (
	"context"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/chatmedia"
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
	var infos, replies []string
	events.Subscribe(bus, func(e events.Info) { infos = append(infos, e.Message) })
	events.Subscribe(bus, func(e events.ResponseReady) { replies = append(replies, e.Response) })
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
	if len(replies) != 1 || replies[0] != "ok" {
		t.Fatalf("the notice was recorded as part of the reply: %q", replies)
	}
	for _, turn := range b.history {
		if strings.Contains(turn.Content, "hosted service") {
			t.Fatalf("the notice entered history: %+v", turn)
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

func TestHostedNoticeIsRecordedOnlyWhenItsRequestGoesAhead(t *testing.T) {
	b := &streamClock{}
	p := &Pipeline{Brain: b, Events: events.NewBus()}
	p.AdmitInput = func(_ context.Context, input string, _ bool) (ResponseAdmission, error) {
		return ResponseAdmission{Request: input, Envelope: events.ResponseEnvelope{ResponseID: "r1", PersonaID: "mira", Hosted: true}}, nil
	}
	var delivered []time.Time
	stop := func() {}
	p.HostedNotice = func(events.ResponseEnvelope) (HostedNotice, bool) {
		stop()
		return HostedNotice{Text: "Mira runs on Claude, a hosted service.", Delivered: func() { delivered = append(delivered, time.Now()) }}, true
	}

	ctx, cancel := context.WithCancel(t.Context())
	stop = cancel
	if err := p.RunTurnTextMode(ctx, "hello"); err == nil {
		t.Fatal("a stopped turn reported success")
	}
	if len(delivered) != 0 || len(b.at) != 0 {
		t.Fatalf("a notice whose turn stopped was recorded (%d) or its request sent (%d)", len(delivered), len(b.at))
	}

	stop = func() {}
	if err := p.RunTurnTextMode(t.Context(), "hello"); err != nil {
		t.Fatal(err)
	}
	if len(delivered) != 1 || len(b.at) != 1 || !delivered[0].Before(b.at[0]) {
		t.Fatalf("delivered %d times, requests %d: the notice must be recorded once, before the request", len(delivered), len(b.at))
	}
}

func TestCompactionAnnouncesAHostedLivePersonaFirst(t *testing.T) {
	const notice = "Mira runs on Claude, a hosted service."
	history := []brain.Turn{{Role: "user", Content: "hi"}, {Role: "assistant", Content: "hello"}}
	b := &compactBrain{history: slices.Clone(history), summary: "Summary."}
	bus := events.NewBus()
	var infos []string
	requestsAtNotice := -1
	events.Subscribe(bus, func(e events.Info) {
		if e.Message == notice {
			infos = append(infos, e.Message)
			requestsAtNotice = len(b.thinkInputs)
		}
	})
	p := &Pipeline{Brain: b, Events: bus, CompactPrompt: "Summarize."}
	ctx, cancel := context.WithCancel(t.Context())
	delivered := 0
	p.LiveHostedNotice = func() (HostedNotice, bool) {
		return HostedNotice{Text: notice, Delivered: func() { delivered++ }}, delivered == 0
	}
	stopped := p.LiveHostedNotice
	p.LiveHostedNotice = func() (HostedNotice, bool) { cancel(); return stopped() }
	if err := p.CompactConversation(ctx); err == nil {
		t.Fatal("compaction stopped during its notice reported success")
	}
	if len(b.thinkInputs) != 0 || delivered != 0 {
		t.Fatalf("stopped compaction sent %d requests and recorded the notice %d times", len(b.thinkInputs), delivered)
	}

	p.LiveHostedNotice = stopped
	if err := p.CompactConversation(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(infos) != 2 || requestsAtNotice != 0 || len(b.thinkInputs) != 1 || delivered != 1 {
		t.Fatalf("infos %q, requests at notice %d, requests %d, delivered %d", infos, requestsAtNotice, len(b.thinkInputs), delivered)
	}
}

// The notice is recorded only when a request goes out: a turn that fails its
// image check, or that is routed away from the persona's provider, sends
// nothing and records nothing.
func TestHostedNoticeIsRecordedOnlyWhenARequestGoesOut(t *testing.T) {
	const notice = "Mira runs on Claude, a hosted service."
	b := &streamClock{}
	b.history = []brain.Turn{{Role: "user", Content: "Look.", Attachments: []chatmedia.Ref{{ID: "gone", Name: "gone.png", Format: "png"}}}}
	bus := events.NewBus()
	var infos []string
	events.Subscribe(bus, func(e events.Info) { infos = append(infos, e.Message) })
	p := &Pipeline{Brain: b, Events: bus}
	p.AdmitInput = func(_ context.Context, input string, _ bool) (ResponseAdmission, error) {
		return ResponseAdmission{Request: input, Envelope: events.ResponseEnvelope{ResponseID: "r1", PersonaID: "mira", Hosted: true}}, nil
	}
	var delivered []time.Time
	p.HostedNotice = func(events.ResponseEnvelope) (HostedNotice, bool) {
		return HostedNotice{Text: notice, Delivered: func() { delivered = append(delivered, time.Now()) }}, len(delivered) == 0
	}

	if err := p.RunTurnTextMode(t.Context(), "hello"); err == nil {
		t.Fatal("a turn whose retained image fails its check went ahead")
	}
	if len(delivered) != 0 || len(b.at) != 0 || slices.Contains(infos, notice) {
		t.Fatalf("a turn that sent nothing gave the notice (%q) or recorded it %d times", infos, len(delivered))
	}

	b.history[0].Attachments = nil
	_ = p.RunTurnTextMode(t.Context(), "tell me a long story")
	if len(delivered) != 0 || len(b.at) != 0 || slices.Contains(infos, notice) {
		t.Fatalf("a routed turn gave the notice (%q) or recorded it %d times, requests %d", infos, len(delivered), len(b.at))
	}

	if err := p.RunTurnTextMode(t.Context(), "hello"); err != nil {
		t.Fatal(err)
	}
	if len(delivered) != 1 || len(b.at) != 1 || !delivered[0].Before(b.at[0]) {
		t.Fatalf("delivered %d times, requests %d: the notice must be recorded once, before the request", len(delivered), len(b.at))
	}
}

// An idle delivery that words a job's result without asking the persona's
// provider sends that provider nothing, so it gives no hosted notice; the
// persona's next request does.
func TestIdleDeliveryThatSendsNothingGivesNoHostedNotice(t *testing.T) {
	const notice = "Mira runs on Claude, a hosted service."
	p, _, _ := newCompletionLeasePipeline(t, false)
	p.AdmitInput = func(_ context.Context, input string, _ bool) (ResponseAdmission, error) {
		return ResponseAdmission{Request: input, Envelope: events.ResponseEnvelope{ResponseID: "r1", PersonaID: "mira", Hosted: true}}, nil
	}
	var infos []string
	var started int
	events.Subscribe(p.Events, func(e events.Info) { infos = append(infos, e.Message) })
	events.Subscribe(p.Events, func(events.ThinkingStarted) { started++ })
	delivered := 0
	p.HostedNotice = func(events.ResponseEnvelope) (HostedNotice, bool) {
		return HostedNotice{Text: notice, Delivered: func() { delivered++ }}, delivered == 0
	}
	if err := p.RunBackgroundTurn(t.Context()); err != nil {
		t.Fatal(err)
	}
	if started != 1 || delivered != 0 || slices.Contains(infos, notice) {
		t.Fatalf("deliveries %d; a delivery that sent nothing gave the notice (%q) or recorded it %d times", started, infos, delivered)
	}
	if err := p.RunTurnTextMode(t.Context(), "hello"); err != nil {
		t.Fatal(err)
	}
	if delivered != 1 {
		t.Fatalf("the next request recorded the notice %d times", delivered)
	}
}

// The voice turn gives the notice in the same place as the text turn: only
// for a request that goes out.
func TestHostedNoticeIsRecordedOnlyWhenAVoiceRequestGoesOut(t *testing.T) {
	const notice = "Mira runs on Claude, a hosted service."
	b := &streamClock{}
	b.history = []brain.Turn{{Role: "user", Content: "Look.", Attachments: []chatmedia.Ref{{ID: "gone", Name: "gone.png", Format: "png"}}}}
	bus := events.NewBus()
	var infos []string
	events.Subscribe(bus, func(e events.Info) { infos = append(infos, e.Message) })
	p := &Pipeline{Brain: b, Events: bus}
	p.AdmitInput = func(_ context.Context, input string, _ bool) (ResponseAdmission, error) {
		return ResponseAdmission{Request: input, Envelope: events.ResponseEnvelope{ResponseID: "r1", PersonaID: "mira", Hosted: true}}, nil
	}
	var delivered []time.Time
	p.HostedNotice = func(events.ResponseEnvelope) (HostedNotice, bool) {
		return HostedNotice{Text: notice, Delivered: func() { delivered = append(delivered, time.Now()) }}, len(delivered) == 0
	}
	say := func(text string) error {
		p.STT = &fakeSTT{text: text}
		_, err := p.RunTurn(t.Context())
		return err
	}

	if err := say("hello"); err == nil {
		t.Fatal("a turn whose retained image fails its check went ahead")
	}
	if len(delivered) != 0 || len(b.at) != 0 || slices.Contains(infos, notice) {
		t.Fatalf("a turn that sent nothing gave the notice (%q) or recorded it %d times", infos, len(delivered))
	}
	b.history[0].Attachments = nil
	_ = say("tell me a long story")
	if len(delivered) != 0 || len(b.at) != 0 || slices.Contains(infos, notice) {
		t.Fatalf("a routed turn gave the notice (%q) or recorded it %d times, requests %d", infos, len(delivered), len(b.at))
	}
	if err := say("hello"); err != nil {
		t.Fatal(err)
	}
	if len(delivered) != 1 || len(b.at) != 1 || !delivered[0].Before(b.at[0]) {
		t.Fatalf("delivered %d times, requests %d: the notice must be recorded once, before the request", len(delivered), len(b.at))
	}
}
