package pipeline

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/chatmedia"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/job"
)

type recordedTranscript struct {
	mu        sync.Mutex
	turns     []brain.Turn
	appendErr error
}

func (r *recordedTranscript) Turns() []brain.Turn {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.turns)
}

func (r *recordedTranscript) Append(_ string, turns ...brain.Turn) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.appendErr != nil {
		return r.appendErr
	}
	r.turns = append(r.turns, turns...)
	return nil
}

func (r *recordedTranscript) Clear() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.turns = nil
}

func (r *recordedTranscript) SetReplyMedia(string, []chatmedia.Ref) error { return nil }

// stallingBrain delivers one chunk, then holds the request open until it is
// canceled, as a CLI provider does when the user interrupts it.
type stallingBrain struct {
	fakeBrain
	partial string
}

func (b *stallingBrain) ThinkStream(ctx context.Context, _ string, _ brain.StreamOptions) (*brain.Stream, error) {
	out, done := make(chan string, 1), make(chan brain.StreamResult, 1)
	go func() {
		defer close(out)
		defer close(done)
		out <- b.partial
		<-ctx.Done()
		done <- brain.StreamResult{Err: ctx.Err()}
	}()
	return &brain.Stream{Chunks: out, Done: done}, nil
}

func admitAs(responseID string, request func(string) string) func(context.Context, string, bool) (ResponseAdmission, error) {
	return func(_ context.Context, input string, _ bool) (ResponseAdmission, error) {
		return ResponseAdmission{Request: request(input), Envelope: events.ResponseEnvelope{SessionID: "s", ResponseID: responseID, PersonaID: "mira", DisplayName: "Mira", PromptHash: "mira-prompt"}}, nil
	}
}

func TestHostTranscriptRecordsProviderReplyUnderAdmittedIdentity(t *testing.T) {
	transcript := &recordedTranscript{}
	p := &Pipeline{Brain: &fakeBrain{chunks: []string{"The door ", "is teal."}}, Events: events.NewBus(), Transcript: transcript}
	p.AdmitInput = admitAs("response-1", func(string) string { return "what color is the door?" })
	var ready events.ResponseReady
	events.Subscribe(p.Events, func(e events.ResponseReady) { ready = e })
	if err := p.RunTurnTextMode(t.Context(), "Mira, what color is the door?"); err != nil {
		t.Fatal(err)
	}
	turns := transcript.Turns()
	if len(turns) != 2 {
		t.Fatalf("transcript = %+v", turns)
	}
	user, reply := turns[0], turns[1]
	if user.Role != "user" || user.Content != "Mira, what color is the door?" || user.RoutedInput != "what color is the door?" {
		t.Fatalf("user turn = %+v", user)
	}
	if ready.Envelope == nil || reply.Role != "assistant" || reply.Content != ready.Response || reply.ResponseID != ready.Envelope.ResponseID ||
		reply.PersonaID != "mira" || reply.PersonaDisplayName != "Mira" || reply.PromptHash != "mira-prompt" || reply.Disposition != "" {
		t.Fatalf("reply = %+v, ready = %+v", reply, ready)
	}
}

func TestHostTranscriptRecordsInterruptedReplyBeforeTheTurnReturns(t *testing.T) {
	transcript := &recordedTranscript{}
	b := &stallingBrain{partial: "The vault code is"}
	p := &Pipeline{Brain: b, Events: events.NewBus(), Transcript: transcript}
	p.AdmitInput = admitAs("response-1", func(input string) string { return input })
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var once sync.Once
	events.Subscribe(p.Events, func(events.ResponseDelta) { once.Do(cancel) })
	if err := p.RunTurnTextMode(ctx, "What is the vault code?"); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	turns := transcript.Turns()
	if len(turns) != 2 || turns[0].Content != "What is the vault code?" || turns[1].Content != b.partial || turns[1].Disposition != "interrupted" || turns[1].ResponseID != "response-1" {
		t.Fatalf("interrupted transcript = %+v", turns)
	}

	p.Brain = &fakeBrain{chunks: []string{"It is cobalt."}}
	p.AdmitInput = admitAs("response-2", func(input string) string { return input })
	if err := p.RunTurnTextMode(t.Context(), "Try again."); err != nil {
		t.Fatal(err)
	}
	turns = transcript.Turns()
	if len(turns) != 4 || turns[2].Content != "Try again." || turns[3].Content != "It is cobalt." || turns[3].Disposition != "" || turns[3].ResponseID != "response-2" {
		t.Fatalf("transcript after the interruption = %+v", turns)
	}
}

func TestHostTranscriptLeavesSharedHistoryProvidersAlone(t *testing.T) {
	transcript := &recordedTranscript{}
	b := &routedProjectionBrain{fakeBrain: &fakeBrain{chunks: []string{"Kept by the provider."}}}
	p := &Pipeline{Brain: b, Events: events.NewBus(), Transcript: transcript}
	p.AdmitInput = admitAs("response-1", func(input string) string { return input })
	if err := p.RunTurnTextMode(t.Context(), "Hello."); err != nil {
		t.Fatal(err)
	}
	if turns := transcript.Turns(); len(turns) != 0 {
		t.Fatalf("host recorded a provider that keeps shared history: %+v", turns)
	}
}

func TestHostTranscriptRecordsLocalAcknowledgement(t *testing.T) {
	transcript := &recordedTranscript{}
	p := &Pipeline{Brain: &fakeBrain{}, Events: events.NewBus(), Transcript: transcript}
	p.AdmitInput = func(_ context.Context, input string, _ bool) (ResponseAdmission, error) {
		return ResponseAdmission{Request: "", LocalReply: "Mira here.", Envelope: events.ResponseEnvelope{ResponseID: "ack", PersonaID: "mira", DisplayName: "Mira"}}, nil
	}
	if err := p.RunTurnTextMode(t.Context(), "Mira."); err != nil {
		t.Fatal(err)
	}
	turns := transcript.Turns()
	if len(turns) != 2 || turns[0].Content != "Mira." || turns[1].Content != "Mira here." || turns[1].ResponseID != "ack" || turns[1].PersonaID != "mira" {
		t.Fatalf("acknowledgement transcript = %+v", turns)
	}
}

func TestHostTranscriptRecordsVoiceTurns(t *testing.T) {
	transcript := &recordedTranscript{}
	p := &Pipeline{STT: &fakeSTT{text: "what's for dinner"}, Brain: &fakeBrain{chunks: []string{"Soup tonight."}}, VAD: &fakeVAD{}, Capture: newFakeCapture(), Events: events.NewBus(), Transcript: transcript}
	p.AdmitInput = admitAs("voice-1", func(input string) string { return input })
	if _, err := p.RunTurn(t.Context()); err != nil {
		t.Fatal(err)
	}
	turns := transcript.Turns()
	if len(turns) != 2 || turns[0].Content != "what's for dinner" || turns[1].Content != "Soup tonight." || turns[1].ResponseID != "voice-1" || turns[1].Disposition != "" {
		t.Fatalf("voice transcript = %+v", turns)
	}

	p.Brain = &stallingBrain{partial: "Soup, and then"}
	p.AdmitInput = admitAs("voice-2", func(input string) string { return input })
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var once sync.Once
	events.Subscribe(p.Events, func(events.ResponseDelta) { once.Do(cancel) })
	_, _ = p.RunTurn(ctx)
	turns = transcript.Turns()
	if len(turns) != 4 || turns[3].Content != "Soup, and then" || turns[3].Disposition != "interrupted" || turns[3].ResponseID != "voice-2" {
		t.Fatalf("interrupted voice transcript = %+v", turns)
	}
}

func TestHostTranscriptRecordsRoutedReplies(t *testing.T) {
	envelope := events.ResponseEnvelope{ResponseID: "routed", PersonaID: "mira", DisplayName: "Mira"}
	t.Run("deterministic route records the request", func(t *testing.T) {
		transcript := &recordedTranscript{turns: []brain.Turn{{Role: "user", Content: "earlier"}}}
		p := &Pipeline{Brain: &fakeBrain{}, Transcript: transcript}
		m := newTurnMetrics()
		m.originalInput, m.historyAtAdmission, m.response = "Mira, start a worker on the report.", 1, envelope
		if err := p.appendRoutedHistory(m, "start a worker on the report.", "Started."); err != nil {
			t.Fatal(err)
		}
		turns := transcript.Turns()
		if len(turns) != 3 || turns[1].Content != m.originalInput || turns[1].RoutedInput != "start a worker on the report." || turns[2].Content != "Started." || turns[2].ResponseID != "routed" {
			t.Fatalf("transcript = %+v", turns)
		}
	})
	t.Run("route after a model reply adds only the reply", func(t *testing.T) {
		transcript := &recordedTranscript{turns: []brain.Turn{{Role: "user", Content: "Do the report."}, {Role: "assistant", Content: "I'll start that.", ResponseID: "routed"}}}
		p := &Pipeline{Brain: &fakeBrain{}, Transcript: transcript}
		m := newTurnMetrics()
		m.originalInput, m.historyAtAdmission, m.response = "Do the report.", 0, envelope
		if err := p.appendRoutedHistory(m, "Do the report.", "Started."); err != nil {
			t.Fatal(err)
		}
		if turns := transcript.Turns(); len(turns) != 3 || turns[2].Content != "Started." || turns[2].PersonaID != "mira" {
			t.Fatalf("transcript = %+v", turns)
		}
	})
}

// slowStopBrain delivers part of a reply and then, once canceled, takes a
// while to report it, like a CLI whose child process is slow to exit. Later
// requests answer at once. It records any request that overlaps another.
type slowStopBrain struct {
	fakeBrain
	stop     time.Duration
	calls    atomic.Int32
	active   atomic.Int32
	overlaps atomic.Int32
}

func (b *slowStopBrain) ThinkStream(ctx context.Context, _ string, _ brain.StreamOptions) (*brain.Stream, error) {
	if b.active.Add(1) > 1 {
		b.overlaps.Add(1)
	}
	first := b.calls.Add(1) == 1
	out, done := make(chan string, 1), make(chan brain.StreamResult, 1)
	go func() {
		defer close(out)
		defer close(done)
		if !first {
			out <- "Second answer."
			b.active.Add(-1)
			done <- brain.StreamResult{}
			return
		}
		out <- "The vault code is"
		<-ctx.Done()
		time.Sleep(b.stop)
		b.active.Add(-1)
		done <- brain.StreamResult{Err: ctx.Err()}
	}()
	return &brain.Stream{Chunks: out, Done: done}, nil
}

func TestHostTranscriptInterruptDoesNotWaitForProviderToStop(t *testing.T) {
	transcript := &recordedTranscript{}
	b := &slowStopBrain{stop: 1500 * time.Millisecond}
	p := &Pipeline{Brain: b, Events: events.NewBus(), Transcript: transcript}
	p.AdmitInput = admitAs("first", func(input string) string { return input })
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var once sync.Once
	var interrupted time.Time
	events.Subscribe(p.Events, func(events.ResponseDelta) {
		once.Do(func() { interrupted = time.Now(); cancel() })
	})
	_ = p.RunTurnTextMode(ctx, "What is the vault code?")
	if waited := time.Since(interrupted); waited > 750*time.Millisecond {
		t.Fatalf("interrupted turn waited %v for the provider to stop", waited)
	}
	turns := transcript.Turns()
	if len(turns) != 2 || turns[1].Content != "The vault code is" || turns[1].Disposition != "interrupted" || turns[1].ResponseID != "first" {
		t.Fatalf("interrupted reply was not recorded when the turn returned: %+v", turns)
	}

	p.AdmitInput = admitAs("second", func(input string) string { return input })
	if err := p.RunTurnTextMode(t.Context(), "Try again."); err != nil {
		t.Fatal(err)
	}
	if n := b.overlaps.Load(); n != 0 {
		t.Fatalf("next request ran while the stopped one was still ending (%d overlaps)", n)
	}
	turns = transcript.Turns()
	if len(turns) != 4 || turns[3].Content != "Second answer." || turns[3].ResponseID != "second" {
		t.Fatalf("stopped reply was recorded again or out of order: %+v", turns)
	}
}

func TestHostTranscriptKeepsReplyCompleteWhenInterruptedAfterProviderFinished(t *testing.T) {
	transcript := &recordedTranscript{}
	p := &Pipeline{Brain: &fakeBrain{chunks: []string{"All done."}}, Events: events.NewBus(), Transcript: transcript}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	stream, recording, err := p.startRecordedStream(ctx, "Finish it.", brain.StreamOptions{Identity: &brain.TurnIdentity{ResponseID: "done"}})
	if err != nil {
		t.Fatal(err)
	}
	for range stream.Chunks {
	}
	<-stream.Done
	cancel()
	recording.settle()
	turns := transcript.Turns()
	if len(turns) != 2 || turns[1].Content != "All done." || turns[1].Disposition != "" {
		t.Fatalf("finished reply = %+v", turns)
	}
}

func TestHostTranscriptAppendFailureIsReported(t *testing.T) {
	transcript := &recordedTranscript{appendErr: errors.New("transcript closed")}
	p := &Pipeline{Brain: &fakeBrain{chunks: []string{"Reply."}}, Events: events.NewBus(), Transcript: transcript}
	p.AdmitInput = admitAs("lost", func(input string) string { return input })
	var reported []events.Error
	events.Subscribe(p.Events, func(e events.Error) { reported = append(reported, e) })
	if err := p.RunTurnTextMode(t.Context(), "Hello."); err != nil {
		t.Fatal(err)
	}
	if len(reported) != 1 || reported[0].Stage != "history" || !strings.Contains(reported[0].Message, "transcript closed") {
		t.Fatalf("lost record was not reported: %+v", reported)
	}
}

// appendingBrain is a provider without shared history that accepts
// host-authored replies, as the Claude, Grok and Cursor sessions do.
type appendingBrain struct {
	fakeBrain
	mu       sync.Mutex
	appended []string
}

func (b *appendingBrain) AppendAssistantResponse(text string) error {
	return b.AppendAssistantResponseWithIdentity(text, brain.TurnIdentity{})
}

func (b *appendingBrain) AppendAssistantResponseWithIdentity(text string, _ brain.TurnIdentity) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.appended = append(b.appended, text)
	return nil
}

func TestHostTranscriptRecordsWorkerReplyOnce(t *testing.T) {
	transcript := &recordedTranscript{}
	b := &appendingBrain{}
	p := &Pipeline{Brain: b, Events: events.NewBus(), Transcript: transcript}
	if err := p.BeginConversationIdentity("worker"); err != nil {
		t.Fatal(err)
	}
	jobs := job.NewTable(t.Context())
	t.Cleanup(jobs.Close)
	a := NewAgency(p, jobs, nil, &config.Config{}, nil)
	a.Wire()
	a.mu.Lock()
	a.workerReplies = append(a.workerReplies, workerReply{workerOrigin: workerOrigin{Envelope: events.ResponseEnvelope{PersonaID: "mira", DisplayName: "Mira", ResponseID: "worker-1"}}, Text: "The report is ready."})
	a.mu.Unlock()
	if err := p.commitWorkerReplies(); err != nil {
		t.Fatal(err)
	}
	turns := transcript.Turns()
	if len(turns) != 1 || turns[0].Content != "The report is ready." || turns[0].PersonaID != "mira" || turns[0].ResponseID != "worker-1" || len(b.appended) != 1 {
		t.Fatalf("worker reply transcript = %+v, provider = %v", turns, b.appended)
	}
}

func TestHostTranscriptRecordsOrdinaryCompletionBriefOnce(t *testing.T) {
	p, c, scope := newCompletionLeasePipeline(t, false)
	transcript := &recordedTranscript{}
	b := &appendingBrain{fakeBrain: fakeBrain{chunks: []string{"Bring a jacket."}}}
	p.Brain, p.Transcript = b, transcript
	if err := p.RunTurnTextMode(t.Context(), "Should I bring a jacket?"); err != nil {
		t.Fatal(err)
	}
	turns := transcript.Turns()
	if len(turns) != 3 || turns[1].Content != "Bring a jacket." || !strings.Contains(turns[2].Content, "pending result") || len(b.appended) != 1 {
		t.Fatalf("ordinary completion transcript = %+v, provider = %v", turns, b.appended)
	}
	if s := c.Snapshot(scope); len(s.Communicated) != 1 {
		t.Fatalf("result not communicated: %+v", s)
	}
}

func TestHostTranscriptRecordsBackgroundCompletionOnce(t *testing.T) {
	p, c, scope := newCompletionLeasePipeline(t, false)
	transcript := &recordedTranscript{}
	b := &appendingBrain{}
	p.Brain, p.Transcript = b, transcript
	if err := p.RunBackgroundTurn(t.Context()); err != nil {
		t.Fatal(err)
	}
	turns := transcript.Turns()
	if len(turns) != 1 || !strings.Contains(turns[0].Content, "pending result") || len(b.appended) != 1 {
		t.Fatalf("background completion transcript = %+v, provider = %v", turns, b.appended)
	}
	if s := c.Snapshot(scope); len(s.Communicated) != 1 {
		t.Fatalf("result not communicated: %+v", s)
	}
}
