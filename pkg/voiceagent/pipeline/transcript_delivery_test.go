package pipeline

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/internal/testmedia"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/chatmedia"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

// heldBrain hands each request's channels to the test, which decides
// what the provider sends and when.
type heldBrain struct {
	fakeBrain
	streams chan *heldRequest
}

type heldRequest struct {
	ctx    context.Context
	chunks chan string
	done   chan brain.StreamResult
}

func newHeldBrain() *heldBrain {
	return &heldBrain{streams: make(chan *heldRequest, 4)}
}

func (b *heldBrain) ThinkStream(ctx context.Context, _ string, _ brain.StreamOptions) (*brain.Stream, error) {
	s := &heldRequest{ctx: ctx, chunks: make(chan string, 16), done: make(chan brain.StreamResult, 1)}
	b.streams <- s
	return &brain.Stream{Chunks: s.chunks, Done: s.done}, nil
}

func recordedReply(t *testing.T, transcript *recordedTranscript) brain.Turn {
	t.Helper()
	for _, turn := range transcript.Turns() {
		if turn.Role == "assistant" {
			return turn
		}
	}
	t.Fatalf("no reply recorded: %+v", transcript.Turns())
	return brain.Turn{}
}

func TestHostTranscriptRecordsOnlyTextTheTurnRead(t *testing.T) {
	identity := &brain.TurnIdentity{ResponseID: "r"}
	t.Run("text sent after the turn stopped", func(t *testing.T) {
		for range 30 {
			transcript := &recordedTranscript{}
			b := newHeldBrain()
			p := &Pipeline{Brain: b, Events: events.NewBus(), Transcript: transcript}
			ctx, cancel := context.WithCancel(t.Context())
			stream, recording, err := p.startRecordedStream(ctx, "What is the code?", brain.StreamOptions{Identity: identity})
			if err != nil {
				t.Fatal(err)
			}
			s := <-b.streams
			s.chunks <- "Heard. "
			<-stream.Chunks
			cancel()
			s.chunks <- "Never read."
			close(s.chunks)
			s.done <- brain.StreamResult{Err: context.Canceled}
			recording.settle()
			if reply := recordedReply(t, transcript); reply.Content != "Heard." || reply.Disposition != "interrupted" {
				t.Fatalf("recorded reply = %+v", reply)
			}
		}
	})
	t.Run("text sent before the turn stopped but never read", func(t *testing.T) {
		transcript := &recordedTranscript{}
		b := newHeldBrain()
		p := &Pipeline{Brain: b, Events: events.NewBus(), Transcript: transcript}
		ctx, cancel := context.WithCancel(t.Context())
		stream, recording, err := p.startRecordedStream(ctx, "What is the code?", brain.StreamOptions{Identity: identity})
		if err != nil {
			t.Fatal(err)
		}
		s := <-b.streams
		s.chunks <- "Heard. "
		<-stream.Chunks
		s.chunks <- "Waiting one. "
		s.chunks <- "Waiting two."
		for len(s.chunks) == 2 {
			time.Sleep(time.Millisecond)
		}
		time.Sleep(20 * time.Millisecond)
		cancel()
		close(s.chunks)
		s.done <- brain.StreamResult{Err: context.Canceled}
		recording.settle()
		if reply := recordedReply(t, transcript); reply.Content != "Heard." || reply.Disposition != "interrupted" {
			t.Fatalf("recorded reply = %+v", reply)
		}
	})
}

// lateResultBrain closes its text stream and reports success only after a
// pause longer than the recording grace, as a CLI does while it writes its
// final result.
type lateResultBrain struct {
	appendingBrain
	delay time.Duration
}

func (b *lateResultBrain) ThinkStream(context.Context, string, brain.StreamOptions) (*brain.Stream, error) {
	out, done := make(chan string, len(b.chunks)), make(chan brain.StreamResult, 1)
	go func() {
		defer close(done)
		for _, c := range b.chunks {
			out <- c
		}
		close(out)
		time.Sleep(b.delay)
		done <- brain.StreamResult{}
	}()
	return &brain.Stream{Chunks: out, Done: done}, nil
}

func TestHostTranscriptRecordsCompleteReplyWhenTheResultFollowsTheLastText(t *testing.T) {
	const delay = 3 * recordingGrace
	late := func() *lateResultBrain {
		return &lateResultBrain{appendingBrain: appendingBrain{fakeBrain: fakeBrain{chunks: []string{"The door ", "is teal."}}}, delay: delay}
	}
	check := func(t *testing.T, transcript *recordedTranscript) {
		t.Helper()
		reply := recordedReply(t, transcript)
		if reply.Content != "The door is teal." || reply.Disposition != "" || reply.ResponseID != "late" {
			t.Fatalf("reply = %+v", reply)
		}
	}
	t.Run("text turn", func(t *testing.T) {
		transcript := &recordedTranscript{}
		p := &Pipeline{Brain: late(), Events: events.NewBus(), Transcript: transcript}
		p.AdmitInput = admitAs("late", func(input string) string { return input })
		if err := p.RunTurnTextMode(t.Context(), "What color is the door?"); err != nil {
			t.Fatal(err)
		}
		check(t, transcript)
	})
	t.Run("voice turn", func(t *testing.T) {
		transcript := &recordedTranscript{}
		p := &Pipeline{STT: &fakeSTT{text: "what color is the door"}, Brain: late(), VAD: &fakeVAD{}, Capture: newFakeCapture(), Events: events.NewBus(), Transcript: transcript}
		p.AdmitInput = admitAs("late", func(input string) string { return input })
		if _, err := p.RunTurn(t.Context()); err != nil {
			t.Fatal(err)
		}
		check(t, transcript)
	})
	t.Run("turn carrying a work result", func(t *testing.T) {
		p, c, scope := newCompletionLeasePipeline(t, false)
		transcript := &recordedTranscript{}
		p.Brain, p.Transcript = late(), transcript
		p.AdmitInput = admitAs("late", func(input string) string { return input })
		if err := p.RunTurnTextMode(t.Context(), "What color is the door?"); err != nil {
			t.Fatal(err)
		}
		check(t, transcript)
		if s := c.Snapshot(scope); len(s.Communicated) != 1 {
			t.Fatalf("result not communicated: %+v", s)
		}
	})
}

// failingBrain delivers part of a reply and then reports err, either at once
// or, when afterCancel is set, only once the turn has stopped it.
type failingBrain struct {
	fakeBrain
	partial     string
	err         error
	afterCancel bool
}

func (b *failingBrain) ThinkStream(ctx context.Context, _ string, _ brain.StreamOptions) (*brain.Stream, error) {
	out, done := make(chan string, 1), make(chan brain.StreamResult, 1)
	go func() {
		defer close(done)
		out <- b.partial
		close(out)
		if b.afterCancel {
			<-ctx.Done()
		}
		done <- brain.StreamResult{Err: b.err}
	}()
	return &brain.Stream{Chunks: out, Done: done}, nil
}

func TestHostTranscriptKeepsTheProvidersReasonForAnUnfinishedReply(t *testing.T) {
	t.Run("provider fails on its own", func(t *testing.T) {
		transcript := &recordedTranscript{}
		p := &Pipeline{Brain: &failingBrain{partial: "The door is", err: errors.New("claude: exit status 1")}, Events: events.NewBus(), Transcript: transcript}
		p.AdmitInput = admitAs("failed", func(input string) string { return input })
		if err := p.RunTurnTextMode(t.Context(), "What color is the door?"); err != nil {
			t.Fatal(err)
		}
		if reply := recordedReply(t, transcript); reply.Content != "The door is" || reply.Disposition != "failed" {
			t.Fatalf("reply = %+v", reply)
		}
	})
	t.Run("stopped provider reports success", func(t *testing.T) {
		transcript := &recordedTranscript{}
		p := &Pipeline{Brain: &failingBrain{partial: "The door is", afterCancel: true}, Events: events.NewBus(), Transcript: transcript}
		p.AdmitInput = admitAs("stopped", func(input string) string { return input })
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		var once sync.Once
		events.Subscribe(p.Events, func(events.ResponseDelta) { once.Do(cancel) })
		if err := p.RunTurnTextMode(ctx, "What color is the door?"); !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v", err)
		}
		if reply := recordedReply(t, transcript); reply.Content != "The door is" || reply.Disposition != "interrupted" {
			t.Fatalf("reply = %+v", reply)
		}
	})
	t.Run("stopped provider reports its own error", func(t *testing.T) {
		transcript := &recordedTranscript{}
		p := &Pipeline{Brain: &failingBrain{partial: "The door is", err: errors.New("claude: signal: killed"), afterCancel: true}, Events: events.NewBus(), Transcript: transcript}
		p.AdmitInput = admitAs("stopped", func(input string) string { return input })
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		var once sync.Once
		events.Subscribe(p.Events, func(events.ResponseDelta) { once.Do(cancel) })
		if err := p.RunTurnTextMode(ctx, "What color is the door?"); !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v", err)
		}
		if reply := recordedReply(t, transcript); reply.Content != "The door is" || reply.Disposition != "interrupted" {
			t.Fatalf("reply = %+v", reply)
		}
	})
}

// silentEndBrain delivers a reply and closes its result without reporting one.
type silentEndBrain struct {
	fakeBrain
}

func (b *silentEndBrain) ThinkStream(context.Context, string, brain.StreamOptions) (*brain.Stream, error) {
	out, done := make(chan string, 1), make(chan brain.StreamResult)
	out <- "The door is teal."
	close(out)
	close(done)
	return &brain.Stream{Chunks: out, Done: done}, nil
}

func TestHostTranscriptTurnEndsAsItIsRecordedWhenTheProviderSendsNoResult(t *testing.T) {
	transcript := &recordedTranscript{}
	p := &Pipeline{Brain: &silentEndBrain{}, Events: events.NewBus(), Transcript: transcript}
	p.AdmitInput = admitAs("silent", func(input string) string { return input })
	var reported []events.Error
	events.Subscribe(p.Events, func(e events.Error) { reported = append(reported, e) })
	if err := p.RunTurnTextMode(t.Context(), "What color is the door?"); err != nil {
		t.Fatal(err)
	}
	if reply := recordedReply(t, transcript); reply.Disposition != "failed" {
		t.Fatalf("reply = %+v", reply)
	}
	if len(reported) == 0 || !strings.Contains(reported[0].Message, "without a result") {
		t.Fatalf("the turn finished as a success the transcript records as failed: %+v", reported)
	}
}

// routeAfterStopBrain chooses a route only after the turn has stopped it, and
// reports that a little later, so the turn has returned before the route is
// known.
type routeAfterStopBrain struct {
	fakeBrain
	partial string
}

func (b *routeAfterStopBrain) ThinkStream(ctx context.Context, _ string, _ brain.StreamOptions) (*brain.Stream, error) {
	out, done := make(chan string, 1), make(chan brain.StreamResult, 1)
	go func() {
		defer close(done)
		if b.partial != "" {
			out <- b.partial
		}
		close(out)
		<-ctx.Done()
		time.Sleep(recordingGrace / 2)
		done <- brain.StreamResult{Route: "spawn"}
	}()
	return &brain.Stream{Chunks: out, Done: done}, nil
}

func TestHostTranscriptKeepsTheRequestOfARoutedTurnThatNeverRouted(t *testing.T) {
	transcript := &recordedTranscript{}
	p := &Pipeline{Brain: &routeAfterStopBrain{partial: "I'll start "}, Events: events.NewBus(), Transcript: transcript}
	p.AdmitInput = admitAs("routed", func(input string) string { return input })
	var routed []RouteDecision
	p.OnRoute = func(d RouteDecision) { routed = append(routed, d) }
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var once sync.Once
	events.Subscribe(p.Events, func(events.ResponseDelta) { once.Do(cancel) })
	if err := p.RunTurnTextMode(ctx, "Start a worker on the report."); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	time.Sleep(2 * recordingGrace)
	turns := transcript.Turns()
	if len(routed) != 0 || len(turns) != 2 || turns[0].Content != "Start a worker on the report." ||
		turns[1].Content != "I'll start" || turns[1].Disposition != "interrupted" || turns[1].ResponseID != "routed" {
		t.Fatalf("routed = %+v, transcript = %+v", routed, turns)
	}
}

func TestHostTranscriptRecordsARoutedTurnOnce(t *testing.T) {
	transcript := &recordedTranscript{}
	p := &Pipeline{Brain: &fakeBrain{chunks: []string{"Looking that up."}, route: "spawn"}, Events: events.NewBus(), Transcript: transcript}
	p.AdmitInput = admitAs("routed", func(input string) string { return input })
	p.OnRoute = func(RouteDecision) {}
	if err := p.RunTurnTextMode(t.Context(), "Look up the report."); err != nil {
		t.Fatal(err)
	}
	turns := transcript.Turns()
	users := 0
	for _, turn := range turns {
		if turn.Role == "user" {
			users++
		}
	}
	if users != 1 || !strings.Contains(turns[0].Content, "Look up the report.") {
		t.Fatalf("routed turn transcript = %+v", turns)
	}
}

// stopCountingBrain holds every request open until it is stopped, then takes
// stop to report it, and counts requests that overlap another.
type stopCountingBrain struct {
	fakeBrain
	stop     time.Duration
	active   atomic.Int32
	overlaps atomic.Int32
}

func (b *stopCountingBrain) ThinkStream(ctx context.Context, _ string, _ brain.StreamOptions) (*brain.Stream, error) {
	if b.active.Add(1) > 1 {
		b.overlaps.Add(1)
	}
	out, done := make(chan string, 1), make(chan brain.StreamResult, 1)
	go func() {
		defer close(done)
		out <- "Partial."
		close(out)
		<-ctx.Done()
		time.Sleep(b.stop)
		b.active.Add(-1)
		done <- brain.StreamResult{Err: ctx.Err()}
	}()
	return &brain.Stream{Chunks: out, Done: done}, nil
}

func TestHostTranscriptRequestsOnOneProviderNeverOverlap(t *testing.T) {
	b := &stopCountingBrain{stop: 300 * time.Millisecond}
	p := &Pipeline{Brain: b, Events: events.NewBus(), Transcript: &recordedTranscript{}}
	p.AdmitInput = admitAs("r", func(input string) string { return input })
	for i := range 3 {
		ctx, cancel := context.WithCancel(t.Context())
		var once sync.Once
		unsubscribe := events.SubscribeDetachable(p.Events, func(events.ResponseDelta) { once.Do(cancel) })
		_ = p.RunTurnTextMode(ctx, fmt.Sprintf("Question %d.", i))
		unsubscribe()
		cancel()
	}
	if err := p.awaitRequests(t.Context(), p.Brain); err != nil {
		t.Fatal(err)
	}
	if n := b.overlaps.Load(); n != 0 {
		t.Fatalf("%d requests started while an earlier one was still ending", n)
	}
}

func TestHostTranscriptNewTurnStopsWaitingForAStuckProvider(t *testing.T) {
	saved := providerStopTimeout
	providerStopTimeout = 100 * time.Millisecond
	t.Cleanup(func() { providerStopTimeout = saved })
	b := &stopCountingBrain{stop: 2 * time.Second}
	p := &Pipeline{Brain: b, Events: events.NewBus(), Transcript: &recordedTranscript{}}
	p.AdmitInput = admitAs("r", func(input string) string { return input })
	var reported []events.Error
	events.Subscribe(p.Events, func(e events.Error) { reported = append(reported, e) })
	ctx, cancel := context.WithCancel(t.Context())
	var once sync.Once
	unsubscribe := events.SubscribeDetachable(p.Events, func(events.ResponseDelta) { once.Do(cancel) })
	_ = p.RunTurnTextMode(ctx, "First question.")
	unsubscribe()
	cancel()

	second, stop := context.WithTimeout(t.Context(), 3*time.Second)
	defer stop()
	started := time.Now()
	err := p.RunTurnTextMode(second, "Second question.")
	if waited := time.Since(started); waited > time.Second {
		t.Fatalf("new turn waited %v for the stopped provider", waited)
	}
	if !errors.Is(err, errProviderStopping) || len(reported) == 0 || reported[len(reported)-1].Message != errProviderStopping.Error() {
		t.Fatalf("err = %v, reported = %+v", err, reported)
	}
	if n := b.overlaps.Load(); n != 0 {
		t.Fatalf("the stuck provider got a second request (%d overlaps)", n)
	}
}

// imageReplyBrain accepts the images the host moves into its reply.
type imageReplyBrain struct {
	fakeBrain
}

func (b *imageReplyBrain) SetReplyMedia(string, []chatmedia.Ref) error { return nil }

func TestHostTranscriptAttachesReplyImagesOnlyToTheReplyTheTurnRecorded(t *testing.T) {
	root := t.TempDir()
	config.SetConfigDirForTest(t, filepath.Join(root, "config"))
	dir := chatmedia.DefaultStore().Dir
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	image := filepath.Join(dir, "chart.gif")
	if err := os.WriteFile(image, testmedia.GIF(), 0o600); err != nil {
		t.Fatal(err)
	}
	response := "Here is the chart.\n![Chart](<" + image + ">)"
	own := brain.Turn{Role: "assistant", Content: response, ResponseID: "chart"}
	later := brain.Turn{Role: "assistant", Content: "The report is ready.", ResponseID: "worker"}
	transcript := &recordedTranscript{turns: []brain.Turn{{Role: "user", Content: "Show me the chart."}, own, later}}
	p := &Pipeline{Brain: &imageReplyBrain{}, Events: events.NewBus(), Transcript: transcript}
	metrics := newTurnMetrics()
	metrics.recording = &transcriptRecording{reply: &own}
	if reply := p.finalizeMedia(metrics, response); len(reply.Attachments) != 1 {
		t.Fatalf("reply images were not imported: %+v", reply)
	}
	turns := transcript.Turns()
	if len(turns[1].Attachments) != 1 || strings.Contains(turns[1].Content, "![") {
		t.Fatalf("the turn's reply did not take its image: %+v", turns[1])
	}
	if len(turns[2].Attachments) != 0 || turns[2].Content != later.Content {
		t.Fatalf("a later reply took the image: %+v", turns[2])
	}

	unrecorded := newTurnMetrics()
	var warned []string
	events.Subscribe(p.Events, func(e events.Info) { warned = append(warned, e.Message) })
	before := transcript.Turns()
	p.finalizeMedia(unrecorded, response)
	if after := transcript.Turns(); len(warned) == 0 || len(after[2].Attachments) != 0 || after[1].Content != before[1].Content {
		t.Fatalf("an unrecorded reply's images went to another reply: warned %v, transcript %+v", warned, after)
	}
}
