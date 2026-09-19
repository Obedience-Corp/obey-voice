package pipeline

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/tts"
)

// Each of these is past laterFloorChars on its own, so the sentence the brain
// emits and the segment the engine speaks are the same thing and a test can
// name an index by its first word.
var lookaheadScript = []string{
	"Alpha reports the nightly build failed on main and the integration tests stopped early. ",
	"Bravo says the failure is in the speech worker handshake and not in the audio pipeline. ",
	"Charlie confirms the fix is already on a branch and the rest of the suite is passing now.",
}

func lookaheadSegments() []string {
	out := make([]string, len(lookaheadScript))
	for i, s := range lookaheadScript {
		out[i] = strings.TrimSpace(s)
	}
	return out
}

// lookaheadFakeTTS blocks the calling worker for synthDelay — a real engine
// holds its provider mutex the same way — and then hands back a stream that
// becomes ready after readyDelay, which is where the wait a worker pays inside
// PlayStream lives. Both are keyed by a segment's first word.
type lookaheadFakeTTS struct {
	concurrency int
	synthDelay  map[string]time.Duration
	readyDelay  map[string]time.Duration
	silent      string
	// fails names a segment this engine refuses, so a fallback test does not
	// depend on which worker reached the provider first.
	fails map[string]bool
	// hold blocks a segment after its completion is recorded and before its
	// worker reaches the gate, so a test can place a cancel in that window.
	hold map[string]chan struct{}

	mu        sync.Mutex
	started   []string
	completed []string
	streams   map[*audio.PCMStream]string
	cancels   atomic.Int32
}

func (f *lookaheadFakeTTS) Synthesize(ctx context.Context, text string) (*audio.PCMStream, error) {
	word := firstWord(text)

	f.mu.Lock()
	f.started = append(f.started, text)
	f.mu.Unlock()

	if d := f.synthDelay[word]; d > 0 {
		select {
		case <-time.After(d):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if word == f.silent {
		return nil, errNothingToSpeak
	}
	if f.fails[word] {
		return nil, &tts.ProviderError{Provider: "lookahead", Kind: tts.ProviderErrorUnavailable, Err: errors.New("worker unavailable")}
	}

	stream, err := (&fakeTTS{delay: f.readyDelay[word]}).Synthesize(ctx, text)
	if err != nil {
		return nil, err
	}
	f.mu.Lock()
	if f.streams == nil {
		f.streams = make(map[*audio.PCMStream]string)
	}
	f.streams[stream] = text
	f.completed = append(f.completed, text)
	f.mu.Unlock()
	if hold := f.hold[word]; hold != nil {
		<-hold
	}
	return stream, nil
}

func (f *lookaheadFakeTTS) Available() bool                       { return true }
func (f *lookaheadFakeTTS) ListVoices(string, string) []tts.Voice { return nil }
func (f *lookaheadFakeTTS) Concurrency() int                      { return f.concurrency }
func (f *lookaheadFakeTTS) SoftCancel(string) error               { f.cancels.Add(1); return nil }

func (f *lookaheadFakeTTS) textOf(stream *audio.PCMStream) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.streams[stream]
}

func (f *lookaheadFakeTTS) snapshot() (started, completed []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.started...), append([]string(nil), f.completed...)
}

func firstWord(s string) string {
	if i := strings.IndexByte(s, ' '); i > 0 {
		return s[:i]
	}
	return s
}

// orderingPlayer records the text behind every stream handed to PlayStream, in
// the order it was handed over. That sequence is the invariant this slice
// defends: it is the order the listener hears.
type orderingPlayer struct {
	*fakePlayer
	textOf func(*audio.PCMStream) string

	mu     sync.Mutex
	played []string
}

func newOrderingPlayer(provider *lookaheadFakeTTS, playDuration time.Duration) *orderingPlayer {
	return &orderingPlayer{fakePlayer: newFakePlayer(playDuration), textOf: provider.textOf}
}

// newTwoVoiceOrderingPlayer resolves a stream through either engine, so a turn
// that switches voices mid-reply still reports what was handed to the player.
func newTwoVoiceOrderingPlayer(primary, fallback *lookaheadFakeTTS, playDuration time.Duration) *orderingPlayer {
	resolve := func(stream *audio.PCMStream) string {
		if text := primary.textOf(stream); text != "" {
			return text
		}
		return fallback.textOf(stream)
	}
	return &orderingPlayer{fakePlayer: newFakePlayer(playDuration), textOf: resolve}
}

func (p *orderingPlayer) PlayStream(ctx context.Context, stream *audio.PCMStream) (*audio.Playback, error) {
	p.mu.Lock()
	p.played = append(p.played, p.textOf(stream))
	p.mu.Unlock()
	return p.fakePlayer.PlayStream(ctx, stream)
}

func (p *orderingPlayer) Played() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.played...)
}

func speakLookaheadScript(t *testing.T, p *Pipeline, player *orderingPlayer) {
	t.Helper()
	metrics := newTurnMetrics()
	turn := p.newTurnConductor(metrics)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, _, err := p.streamResponse(ctx, cancel, scriptedStream(ctx, lookaheadScript, nil), false, metrics, turn, nil); err != nil {
		t.Fatalf("streamResponse: %v", err)
	}
	if got := player.Played(); !equalStrings(got, lookaheadSegments()) {
		t.Fatalf("PlayStream order = %q, want admission order %q", got, lookaheadSegments())
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestPlaybackFollowsAdmissionOrderAtDepthTwo is the refactor's invariant: at
// the shipped depth and one engine, the sequence of PlayStream calls and their
// texts is what it was before the gate existed — the groups the segmenter
// admitted, in order. The grouping tests pin that list; this pins that nothing
// reorders it.
func TestPlaybackFollowsAdmissionOrderAtDepthTwo(t *testing.T) {
	provider := &lookaheadFakeTTS{concurrency: 1}
	player := newOrderingPlayer(provider, time.Millisecond)
	defer player.Close()

	p := &Pipeline{TTS: provider, Player: player, Events: events.NewBus()}
	if depth, workers := p.lookahead(provider); depth != 2 || workers != 1 {
		t.Fatalf("lookahead() = depth %d, workers %d, want the shipped 2 and 1", depth, workers)
	}
	speakLookaheadScript(t, p, player)

	_, completed := provider.snapshot()
	if !equalStrings(completed, lookaheadSegments()) {
		t.Fatalf("synthesis order = %q, want admission order at one worker", completed)
	}
}

// A single-worker turn at tts_lookahead=1 still speaks the whole reply in
// order: the intake gate simply admits one segment at a time.
func TestLookaheadOfOneStillSpeaksTheWholeReply(t *testing.T) {
	provider := &lookaheadFakeTTS{concurrency: 1}
	player := newOrderingPlayer(provider, time.Millisecond)
	defer player.Close()

	p := &Pipeline{TTS: provider, Player: player, Events: events.NewBus(), TTSLookahead: 1}
	if depth, workers := p.lookahead(provider); depth != 1 || workers != 1 {
		t.Fatalf("lookahead() = depth %d, workers %d, want 1 and 1", depth, workers)
	}
	speakLookaheadScript(t, p, player)
}

// TestGateHoldsBackASegmentThatFinishedEarly is the reason the gate exists: a
// second worker finishes index 2 while index 1 is still synthesizing, and the
// listener must still hear 0, 1, 2.
func TestGateHoldsBackASegmentThatFinishedEarly(t *testing.T) {
	provider := &lookaheadFakeTTS{
		concurrency: 2,
		// Bravo is index 1. It occupies its worker long enough that the free
		// worker takes index 2 and finishes it first.
		synthDelay: map[string]time.Duration{"Bravo": 300 * time.Millisecond},
	}
	player := newOrderingPlayer(provider, time.Millisecond)
	defer player.Close()

	p := &Pipeline{TTS: provider, Player: player, Events: events.NewBus()}
	if depth, workers := p.lookahead(provider); depth != 2 || workers != 2 {
		t.Fatalf("lookahead() = depth %d, workers %d, want 2 and 2", depth, workers)
	}
	speakLookaheadScript(t, p, player)

	_, completed := provider.snapshot()
	if len(completed) != 3 {
		t.Fatalf("synthesis completions = %q, want all three", completed)
	}
	if !strings.HasPrefix(completed[1], "Charlie") {
		t.Fatalf("synthesis order = %q, want the third segment finished before the second — "+
			"without that the gate is not the thing keeping playback in order", completed)
	}
}

// A segment the engine has nothing to say for releases the gate on its way
// out, so the segment behind it is still handed off and the turn ends.
func TestNothingToSpeakDoesNotWedgeTheGate(t *testing.T) {
	provider := &lookaheadFakeTTS{
		concurrency: 3,
		silent:      "Bravo",
		// Alpha holds index 0 long enough that index 2 reaches the gate first.
		synthDelay: map[string]time.Duration{"Alpha": 150 * time.Millisecond},
	}
	player := newOrderingPlayer(provider, time.Millisecond)
	defer player.Close()

	p := &Pipeline{TTS: provider, Player: player, Events: events.NewBus()}
	gate := newOrderGate()
	out := make(chan playbackEvent, 16)
	var audioStarted atomic.Bool

	segments := lookaheadSegments()
	done := make(chan segmentSynthesisOutcome, len(segments))
	for i, sentence := range segments {
		now := time.Now()
		work := synthWork{sentence: sentence, index: i, textReady: now, queuedAt: now}
		go func() {
			done <- p.synthesizeSegmentObserved(context.Background(), make(chan struct{}), work, gate, &audioStarted, out, nil)
		}()
	}
	enqueued := 0
	for range segments {
		select {
		case outcome := <-done:
			if outcome.enqueued {
				enqueued++
			}
		case <-time.After(5 * time.Second):
			t.Fatal("a segment never returned — the skipped index wedged the gate")
		}
	}
	if enqueued != 2 {
		t.Fatalf("enqueued playbacks = %d, want the two speakable segments", enqueued)
	}
	want := []string{segments[0], segments[2]}
	if got := player.Played(); !equalStrings(got, want) {
		t.Fatalf("PlayStream order = %q, want %q", got, want)
	}
}

// Cancellation (turn cancel, or the barge-in that cancels the same context)
// drops everything above the index already handed to the player: a parked
// worker releases its index, hands nothing off, and the turn ends.
func TestCancelDropsSegmentsAboveThePlayingOne(t *testing.T) {
	provider := &lookaheadFakeTTS{
		concurrency: 2,
		// Alpha's stream stays unready, so PlayStream holds index 0 and index
		// 1 parks at the gate with a finished stream of its own.
		readyDelay: map[string]time.Duration{"Alpha": 10 * time.Second},
	}
	player := newOrderingPlayer(provider, time.Millisecond)
	defer player.Close()

	p := &Pipeline{TTS: provider, Player: player, Events: events.NewBus()}
	metrics := newTurnMetrics()
	turn := p.newTurnConductor(metrics)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	returned := make(chan error, 1)
	go func() {
		_, _, err := p.streamResponse(ctx, cancel, scriptedStream(ctx, lookaheadScript, nil), false, metrics, turn, nil)
		returned <- err
	}()

	// Cancel only once index 1 is really parked: its synthesis has completed
	// while index 0, the only thing the listener has, is still inside
	// PlayStream.
	segments := lookaheadSegments()
	waitForCond(t, func() bool {
		return completedSegment(provider, "Bravo") && equalStrings(player.Played(), segments[:1])
	}, 3*time.Second, "index 1 never finished synthesizing behind a blocked index 0")

	p.softCancelTTS("test cancel")
	cancel()

	select {
	case <-returned:
	case <-time.After(5 * time.Second):
		t.Fatal("the turn did not end after cancellation — a parked worker wedged it")
	}
	if got := player.Played(); !equalStrings(got, segments[:1]) {
		t.Fatalf("PlayStream calls = %q, want only the index that was already playing", got)
	}
	if provider.cancels.Load() == 0 {
		t.Fatal("SoftCancel never reached the provider")
	}
}

// Cancel outranks an open gate. Index 0's release and the cancel are one
// window on a parked worker, and turn cancel does not stop the player, so a
// stream handed over after the cancel is audible. Driving the workers directly
// pins the order the turn can only race: cancel, then index 0 releases, then
// index 1 reaches the gate.
func TestCancelBeatsAnOpenGateForAParkedSegment(t *testing.T) {
	release := make(chan struct{})
	provider := &lookaheadFakeTTS{
		concurrency: 2,
		// Index 0 sits inside PlayStream on a stream that never becomes ready,
		// so it releases the gate only when the cancel unblocks WaitReady.
		readyDelay: map[string]time.Duration{"Alpha": 10 * time.Second},
		// Index 1 finishes synthesizing but reaches the gate when this test
		// says so: after the cancel, with the gate already open.
		hold: map[string]chan struct{}{"Bravo": release},
	}
	player := newOrderingPlayer(provider, time.Millisecond)
	defer player.Close()

	p := &Pipeline{TTS: provider, Player: player, Events: events.NewBus()}
	gate := newOrderGate()
	out := make(chan playbackEvent, 8)
	var audioStarted atomic.Bool
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	segments := lookaheadSegments()[:2]
	done := make(chan segmentSynthesisOutcome, len(segments))
	for i, sentence := range segments {
		now := time.Now()
		work := synthWork{sentence: sentence, index: i, textReady: now, queuedAt: now}
		go func() {
			done <- p.synthesizeSegmentObserved(ctx, make(chan struct{}), work, gate, &audioStarted, out, nil)
		}()
	}

	waitForCond(t, func() bool {
		return completedSegment(provider, "Bravo") && equalStrings(player.Played(), segments[:1])
	}, 3*time.Second, "index 1 never finished synthesizing behind a blocked index 0")

	cancel()
	awaitOutcome(t, done, "index 0 never returned after cancellation")
	close(release)
	awaitOutcome(t, done, "index 1 never returned after cancellation")

	if got := player.Played(); !equalStrings(got, segments[:1]) {
		t.Fatalf("PlayStream calls = %q, want only the index that was already playing", got)
	}
}

func completedSegment(provider *lookaheadFakeTTS, word string) bool {
	_, completed := provider.snapshot()
	for _, text := range completed {
		if strings.HasPrefix(text, word) {
			return true
		}
	}
	return false
}

func awaitOutcome(t *testing.T, done <-chan segmentSynthesisOutcome, msg string) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal(msg)
	}
}

// The fallback latch is turn-scoped, not worker-scoped: once a segment falls
// back, every index admitted after the switch goes straight to the fallback
// voice whichever worker picks it up. One voice per reply is the point.
//
// The last index is the one this can assert deterministically — it is admitted
// only after the first segment finished playing, so the switch is long since
// latched. An index already in flight on the primary when the switch happens
// keeps its own result (see fallbackLatch), which is unreachable at the
// concurrency every shipped engine declares.
func TestFallbackLatchesAcrossWorkers(t *testing.T) {
	primary := &lookaheadFakeTTS{concurrency: 2, fails: map[string]bool{"Alpha": true}}
	fallback := &lookaheadFakeTTS{concurrency: 1}
	player := newTwoVoiceOrderingPlayer(primary, fallback, time.Millisecond)
	defer player.Close()

	p := &Pipeline{TTS: primary, TTSFallback: fallback, Player: player, Events: events.NewBus()}
	speakLookaheadScript(t, p, player)

	segments := lookaheadSegments()
	started, _ := primary.snapshot()
	for _, text := range started {
		if strings.HasPrefix(text, "Charlie") {
			t.Fatalf("primary was asked for %q after the turn had already switched voices", text)
		}
	}
	spoken, _ := fallback.snapshot()
	if len(spoken) < 2 || spoken[0] != segments[0] || spoken[len(spoken)-1] != segments[2] {
		t.Fatalf("fallback spoke %q, want the failed segment and everything admitted after it", spoken)
	}
}

// Depth can never sit below the engine's concurrency: a worker with no queue
// slot to draw from is a goroutine that never runs.
func TestLookaheadClampsDepthToConcurrency(t *testing.T) {
	for _, tc := range []struct {
		name                   string
		configured             int
		concurrency            int
		wantDepth, wantWorkers int
	}{
		{"unset", 0, 1, 2, 1},
		{"explicit one", 1, 1, 1, 1},
		{"explicit four", 4, 1, 4, 1},
		{"below concurrency", 1, 3, 3, 3},
		{"provider declares none", 0, 0, 2, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &Pipeline{TTSLookahead: tc.configured}
			depth, workers := p.lookahead(&lookaheadFakeTTS{concurrency: tc.concurrency})
			if depth != tc.wantDepth || workers != tc.wantWorkers {
				t.Fatalf("lookahead() = depth %d, workers %d, want %d and %d", depth, workers, tc.wantDepth, tc.wantWorkers)
			}
		})
	}
}

// The gate advances only across a contiguous run of released indices. A skip
// that lands before a slower index ahead of it must not let the one behind it
// pass: that is the difference between "k-1 released" and "everything below k
// released", and getting it wrong reorders speech.
func TestOrderGateAdvancesOnlyOverContiguousReleases(t *testing.T) {
	gate := newOrderGate()
	ctx := context.Background()

	passed := make(chan struct{})
	go func() {
		if err := gate.await(ctx, 2); err != nil {
			t.Errorf("await(2): %v", err)
		}
		close(passed)
	}()

	gate.done(1) // out of order: index 0 is still working
	select {
	case <-passed:
		t.Fatal("index 2 passed the gate while index 0 was still outstanding")
	case <-time.After(50 * time.Millisecond):
	}

	gate.done(0)
	select {
	case <-passed:
	case <-time.After(time.Second):
		t.Fatal("index 2 never passed the gate after every index below it released")
	}

	gate.done(0) // idempotent
	if err := gate.await(ctx, 1); err != nil {
		t.Fatalf("await(1) after release: %v", err)
	}
}

func TestOrderGateAwaitReturnsOnCancel(t *testing.T) {
	gate := newOrderGate()
	ctx, cancel := context.WithCancel(context.Background())
	released := make(chan error, 1)
	go func() { released <- gate.await(ctx, 1) }()

	select {
	case err := <-released:
		t.Fatalf("await(1) returned %v before index 0 released", err)
	case <-time.After(50 * time.Millisecond):
	}
	cancel()
	select {
	case err := <-released:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("await(1) = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("await(1) ignored cancellation; the turn would hang on a parked worker")
	}
}

// A release that lands with — or before — the cancel must not open the gate for
// a canceled turn: nothing above the playing index is handed to the player.
func TestOrderGateAwaitReportsCancelOnAnOpenGate(t *testing.T) {
	t.Run("released before the worker arrives", func(t *testing.T) {
		gate := newOrderGate()
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		gate.done(0)
		if err := gate.await(ctx, 1); !errors.Is(err, context.Canceled) {
			t.Fatalf("await(1) = %v, want context.Canceled", err)
		}
	})

	// The release races the parked worker's wake. The turn is canceled before
	// await is ever called, so context.Canceled is the only correct answer
	// whichever wake wins.
	t.Run("released while the worker is parked", func(t *testing.T) {
		for i := range 200 {
			gate := newOrderGate()
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			parked := make(chan struct{})
			released := make(chan error, 1)
			go func() {
				close(parked)
				released <- gate.await(ctx, 1)
			}()
			<-parked
			gate.done(0)
			select {
			case err := <-released:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("await(1) = %v on iteration %d, want context.Canceled", err, i)
				}
			case <-time.After(time.Second):
				t.Fatalf("await(1) never returned on iteration %d", i)
			}
		}
	})
}

// A nil gate is the single-segment path (synthesizeSegment, text mode): one
// segment is already in order and must not need a gate to proceed.
func TestNilOrderGateIsANoOp(t *testing.T) {
	var gate *orderGate
	if err := gate.await(context.Background(), 7); err != nil {
		t.Fatalf("await on a nil gate: %v", err)
	}
	gate.done(7)
}
