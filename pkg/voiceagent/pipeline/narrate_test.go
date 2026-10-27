package pipeline

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/internal/render"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/job"
)

func testNarrateUnits(lines ...string) []narrateUnit {
	spans := make([]render.RenderSpan, 0, len(lines))
	for _, line := range lines {
		spans = append(spans, render.RenderSpan{Kind: render.SpanSpeech, Text: line})
	}
	return []narrateUnit{{spans: spans}}
}

func TestLiveNarratePlaysPlannedSpans(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "fixture.txt")
	if err := os.WriteFile(source, []byte("placeholder\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	provider := &progressiveFakeTTS{}
	player := newFakePlayer(5 * time.Millisecond)
	defer player.Close()

	jobs := job.NewTable(context.Background())
	p := &Pipeline{TTS: provider, Player: player, Events: events.NewBus()}
	agency := NewAgency(p, jobs, nil, &config.Config{WorkDir: dir}, nil)
	agency.Wire()
	agency.narrator.planSource = func(_ context.Context, got string) ([]narrateUnit, error) {
		if got != source {
			t.Fatalf("plan source = %q, want %q", got, source)
		}
		return testNarrateUnits("Alpha paragraph.", "Beta paragraph."), nil
	}

	ctx := context.Background()
	metrics := newTurnMetrics()
	turn := p.newTurnConductor(metrics)
	agency.narrator.HandleRoute(ctx, turn, metrics, RouteDecision{
		Kind:       RouteNarrate,
		Transcript: "read this " + source,
		Source:     "test",
	})

	waitForTTS(t, provider, 2, 3*time.Second)
	if !agency.narrator.IsActive() {
		t.Fatal("narration mode ended before all spans were spoken")
	}

	deadline := time.After(3 * time.Second)
	for {
		calls := provider.Calls()
		if len(calls) >= 3 {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("TTS calls = %v, want ack and two spans", calls)
		case <-time.After(20 * time.Millisecond):
		}
	}

	calls := provider.Calls()
	if !strings.Contains(strings.ToLower(calls[0]), "read") {
		t.Fatalf("first call = %q, want narrate ack", calls[0])
	}
	joined := strings.Join(calls[1:], " ")
	if !strings.Contains(joined, "Alpha paragraph.") || !strings.Contains(joined, "Beta paragraph.") {
		t.Fatalf("span playback = %v", calls)
	}
}

func TestNarrateBargeInStopsSpeechKeepsMode(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "story.txt")
	if err := os.WriteFile(source, []byte("placeholder\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	provider := &progressiveFakeTTS{}
	player := newFakePlayer(200 * time.Millisecond)
	defer player.Close()

	jobs := job.NewTable(context.Background())
	p := &Pipeline{TTS: provider, Player: player, Events: events.NewBus()}
	agency := NewAgency(p, jobs, nil, &config.Config{WorkDir: dir}, nil)
	agency.Wire()
	agency.narrator.planSource = func(context.Context, string) ([]narrateUnit, error) {
		return testNarrateUnits("First span.", "Second span."), nil
	}

	ctx := context.Background()
	metrics := newTurnMetrics()
	turn := p.newTurnConductor(metrics)
	agency.narrator.HandleRoute(ctx, turn, metrics, RouteDecision{
		Kind:       RouteNarrate,
		Transcript: source,
		Source:     "test",
	})

	waitForTTS(t, provider, 2, 3*time.Second)
	p.StopPlayback()
	time.Sleep(30 * time.Millisecond)

	if !agency.narrator.IsActive() {
		t.Fatal("StopPlayback ended narration mode; want mode until stop reading")
	}
	if player.StopCount() == 0 {
		t.Fatal("StopPlayback did not stop the player")
	}
}

func TestStopReadingEndsNarrationMode(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(source, []byte("placeholder\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	provider := &progressiveFakeTTS{}
	player := newFakePlayer(time.Millisecond)
	defer player.Close()

	jobs := job.NewTable(context.Background())
	p := &Pipeline{TTS: provider, Player: player, Events: events.NewBus()}
	agency := NewAgency(p, jobs, nil, &config.Config{WorkDir: dir}, nil)
	agency.Wire()
	agency.narrator.planSource = func(context.Context, string) ([]narrateUnit, error) {
		return testNarrateUnits("Still reading."), nil
	}

	ctx := context.Background()
	metrics := newTurnMetrics()
	turn := p.newTurnConductor(metrics)
	agency.narrator.HandleRoute(ctx, turn, metrics, RouteDecision{
		Kind:       RouteNarrate,
		Transcript: source,
		Source:     "test",
	})
	waitForTTS(t, provider, 1, 3*time.Second)
	if !agency.narrator.IsActive() {
		t.Fatal("expected narration mode active before stop reading")
	}

	agency.narrator.HandleRoute(ctx, turn, metrics, RouteDecision{
		Kind:       RouteNarrateStop,
		Transcript: "stop reading",
		Source:     "test",
	})
	if agency.narrator.IsActive() {
		t.Fatal("stop reading did not exit narration mode")
	}

	kind, ok := classifyPhrase("pause", RouteContext{NarrationActive: false})
	if ok {
		t.Fatalf("pause matched %q after stop reading; want chat fall through", kind)
	}
}

func TestUnreadableNarrateSourceReportsErrorWithoutSpeech(t *testing.T) {
	provider := &progressiveFakeTTS{}
	player := newFakePlayer(time.Millisecond)
	defer player.Close()

	jobs := job.NewTable(context.Background())
	p := &Pipeline{TTS: provider, Player: player, Events: events.NewBus()}
	agency := NewAgency(p, jobs, nil, &config.Config{}, nil)
	agency.Wire()
	var report events.Error
	events.Subscribe(p.Events, func(e events.Error) { report = e })

	ctx := context.Background()
	metrics := newTurnMetrics()
	turn := p.newTurnConductor(metrics)
	agency.narrator.HandleRoute(ctx, turn, metrics, RouteDecision{
		Kind:       RouteNarrate,
		Transcript: "read this",
		Source:     "test",
	})

	if agency.narrator.IsActive() {
		t.Fatal("unreadable source left narration mode active")
	}
	if len(provider.Calls()) != 0 || !strings.Contains(report.Message, "No narration source") {
		t.Fatalf("speech=%v report=%+v", provider.Calls(), report)
	}
}

func TestSpawnThenNarrateLongStory(t *testing.T) {
	dir := t.TempDir()

	worker := &storyFileWorker{
		started: make(chan struct{}),
		done:    make(chan struct{}),
		body:    "Chapter One. Once upon a time in a test fixture.\n\nChapter Two. The end.",
	}

	provider := &progressiveFakeTTS{}
	player := newFakePlayer(5 * time.Millisecond)
	defer player.Close()

	jobs := job.NewTable(context.Background())
	p := &Pipeline{TTS: provider, Player: player, Events: events.NewBus()}
	agency := NewAgency(p, jobs, worker, &config.Config{WorkDir: dir}, nil)
	agency.Wire()

	ctx := context.Background()
	metrics := newTurnMetrics()
	turn := p.newTurnConductor(metrics)
	agency.HandleRoute(ctx, turn, metrics, RouteDecision{
		Kind:       RouteNarrate,
		Transcript: "tell me a long story",
		Source:     "test",
	})

	waitForTTS(t, provider, 1, 2*time.Second)
	if !strings.Contains(strings.ToLower(provider.Calls()[0]), "working") {
		t.Fatalf("spawn ack = %q", provider.Calls()[0])
	}

	<-worker.started
	close(worker.done)

	deadline := time.After(5 * time.Second)
	for {
		calls := provider.Calls()
		if len(calls) >= 2 && strings.Contains(strings.Join(calls, " "), "Once upon a time") {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("narration never started: %v", provider.Calls())
		case <-time.After(20 * time.Millisecond):
		}
	}
}

func TestLongStoryRouteSkipsLiveThinkStream(t *testing.T) {
	brainProvider := &countingFakeBrain{fakeBrain: fakeBrain{chunks: []string{"How long should the story be?"}}}
	jobs := job.NewTable(context.Background())
	p := &Pipeline{Brain: brainProvider, Events: events.NewBus()}
	agency := NewAgency(p, jobs, &fakeBrain{chunks: []string{"done"}}, &config.Config{WorkDir: t.TempDir()}, nil)
	agency.Wire()

	metrics := newTurnMetrics()
	turn := p.newTurnConductor(metrics)
	kind, ok := classifyPhrase("tell me a long story", p.routeContext())
	if !ok || kind != RouteNarrate {
		t.Fatalf("route = %q, %v; want narrate", kind, ok)
	}
	if err := p.completeRoutedTurn(context.Background(), turn, metrics, "tell me a long story", kind, "phrase"); err != nil {
		t.Fatal(err)
	}

	if brainProvider.thinkCalls != 0 {
		t.Fatalf("ThinkStream called %d times, want 0 for long story phrase", brainProvider.thinkCalls)
	}
	// The story job writes into WorkDir; let it end before TempDir cleanup
	// races the write. The narrator consumes its done callback, so the
	// table is the only place to watch.
	deadline := time.Now().Add(3 * time.Second)
	for jobs.Active() {
		if time.Now().After(deadline) {
			t.Fatal("story job did not finish")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

type storyFileWorker struct {
	started chan struct{}
	done    chan struct{}
	body    string
}

func (w *storyFileWorker) ThinkStream(ctx context.Context, input string, opts brain.StreamOptions) (*brain.Stream, error) {
	return (&fakeBrain{chunks: []string{w.body}}).ThinkStream(ctx, input, opts)
}

func (w *storyFileWorker) ThinkFull(ctx context.Context, input string, opts brain.StreamOptions) (string, error) {
	close(w.started)
	select {
	case <-w.done:
	case <-ctx.Done():
		return "", ctx.Err()
	}
	if !opts.OmitTurnInstruction {
		return "", errTurnCapUsed
	}
	return w.body, nil
}

func (w *storyFileWorker) ClearHistory()            {}
func (w *storyFileWorker) History() []brain.Turn    { return nil }
func (w *storyFileWorker) LoadHistory([]brain.Turn) {}

var errTurnCapUsed = errString("turn instruction must be omitted for story worker")

type errString string

func (e errString) Error() string { return string(e) }

func waitForTTS(t *testing.T, provider *progressiveFakeTTS, n int, timeout time.Duration) {
	t.Helper()
	deadline := time.After(timeout)
	for {
		if len(provider.Calls()) >= n {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("TTS calls = %d, want >= %d (%v)", len(provider.Calls()), n, provider.Calls())
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func TestNarratePauseResumeSkip(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "book.txt")
	if err := os.WriteFile(source, []byte("placeholder\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	provider := &progressiveFakeTTS{}
	player := newFakePlayer(50 * time.Millisecond)
	defer player.Close()

	jobs := job.NewTable(context.Background())
	p := &Pipeline{TTS: provider, Player: player, Events: events.NewBus()}
	agency := NewAgency(p, jobs, nil, &config.Config{WorkDir: dir}, nil)
	agency.Wire()
	agency.narrator.planSource = func(context.Context, string) ([]narrateUnit, error) {
		return []narrateUnit{
			{spans: []render.RenderSpan{{Kind: render.SpanSpeech, Text: "Unit one."}}},
			{spans: []render.RenderSpan{{Kind: render.SpanSpeech, Text: "Unit two."}}},
		}, nil
	}

	ctx := context.Background()
	metrics := newTurnMetrics()
	turn := p.newTurnConductor(metrics)
	agency.narrator.HandleRoute(ctx, turn, metrics, RouteDecision{
		Kind:       RouteNarrate,
		Transcript: source,
		Source:     "test",
	})
	waitForTTS(t, provider, 2, 3*time.Second)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		agency.narrator.HandleRoute(ctx, turn, metrics, RouteDecision{Kind: RouteNarratePause, Transcript: "pause", Source: "test"})
		time.Sleep(20 * time.Millisecond)
		agency.narrator.HandleRoute(ctx, turn, metrics, RouteDecision{Kind: RouteNarrateResume, Transcript: "resume", Source: "test"})
		agency.narrator.HandleRoute(ctx, turn, metrics, RouteDecision{Kind: RouteNarrateSkip, Transcript: "skip", Source: "test"})
	}()
	wg.Wait()

	deadline := time.After(3 * time.Second)
	for {
		if strings.Contains(strings.Join(provider.Calls(), " "), "Unit two.") {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("skip did not reach second unit: %v", provider.Calls())
		case <-time.After(20 * time.Millisecond):
		}
	}
}
