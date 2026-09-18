package pipeline

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/tts"
)

// groupingFakeTTS declares the two capabilities the grouping policy reads. A
// zero ramp means the provider does not declare one.
type groupingFakeTTS struct {
	fakeTTS
	joins bool
	ramp  float64
}

func (g *groupingFakeTTS) JoinsSentences() bool { return g.joins }
func (g *groupingFakeTTS) SegmentRamp() float64 { return g.ramp }

// speakScript runs one turn over a scripted brain stream and returns the texts
// the provider was asked to synthesize.
func speakScript(t *testing.T, provider *groupingFakeTTS, chunks ...string) []string {
	t.Helper()
	calls, _ := runScript(t, provider, chunks...)
	return calls
}

// runScript also returns the transcript the turn produced, for cases where the
// spoken text and the recorded text must differ.
func runScript(t *testing.T, provider *groupingFakeTTS, chunks ...string) ([]string, string) {
	t.Helper()
	player := newFakePlayer(time.Millisecond)
	defer player.Close()
	p := &Pipeline{TTS: provider, Player: player, Events: events.NewBus()}

	metrics := newTurnMetrics()
	turn := p.newTurnConductor(metrics)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	response, _, err := p.streamResponse(ctx, cancel, scriptedStream(ctx, chunks, nil), false, metrics, turn, nil)
	if err != nil {
		t.Fatalf("streamResponse: %v", err)
	}
	return provider.Texts(), response
}

// speakTimed runs a turn whose chunks are separated by pause, with the idle
// flush shortened so the case does not wait out the product's 350 ms.
func speakTimed(t *testing.T, provider *groupingFakeTTS, idle, pause time.Duration, chunks ...string) []string {
	t.Helper()
	player := newFakePlayer(time.Millisecond)
	defer player.Close()
	p := &Pipeline{TTS: provider, Player: player, Events: events.NewBus(), idleFlush: idle}

	in := make(chan string)
	done := make(chan brain.StreamResult, 1)
	go func() {
		defer close(in)
		defer close(done)
		for i, chunk := range chunks {
			if i > 0 {
				time.Sleep(pause)
			}
			in <- chunk
		}
		done <- brain.StreamResult{}
	}()

	metrics := newTurnMetrics()
	turn := p.newTurnConductor(metrics)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, _, err := p.streamResponse(ctx, cancel, &brain.Stream{Chunks: in, Done: done}, false, metrics, turn, nil); err != nil {
		t.Fatalf("streamResponse: %v", err)
	}
	return provider.Texts()
}

// TestTriggerPhraseIsOneSynthesis is the heard defect: "[gasp] Wait. Wait, you
// what?" was spoken as "Wait." and then "Wait, you what?" — two prosody
// samples for one thought, the first paying the turn's cold synthesis cost.
func TestTriggerPhraseIsOneSynthesis(t *testing.T) {
	for _, tc := range []struct {
		name  string
		joins bool
		want  string
	}{
		{
			// Kokoro re-splits on sentence punctuation inside one call, so the
			// beat's period has to become a comma or nothing changes.
			name: "textual join", joins: false, want: "Wait, wait, you what?",
		},
		{
			// Qwen conditions the second sentence on the first, so the request
			// carries the contour and the text is spoken as written.
			name: "request join", joins: true, want: "Wait. Wait, you what?",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provider := &groupingFakeTTS{joins: tc.joins}
			calls := speakScript(t, provider, "[gasp] Wait. ", "Wait, you what?")
			if len(calls) != 1 {
				t.Fatalf("Synthesize calls = %q, want the beat and its follower in one", calls)
			}
			if calls[0] != tc.want {
				t.Fatalf("synthesized %q, want %q", calls[0], tc.want)
			}
		})
	}
}

// A normal opening still goes out at its period: time-to-first-audio waits on
// the opening, and merging it would spend the listener's wait on a sentence
// they do not need yet.
func TestOpeningSentenceFlushesAtItsPeriod(t *testing.T) {
	provider := &groupingFakeTTS{}
	calls := speakScript(t, provider,
		"The build failed on main. ",
		"The second sentence is here. ",
		"The third sentence is here now.",
	)
	if len(calls) < 2 {
		t.Fatalf("Synthesize calls = %q, want the opening on its own", calls)
	}
	if calls[0] != "The build failed on main." {
		t.Fatalf("first synthesis = %q, want the opening sentence alone", calls[0])
	}
}

// A one-beat reply is still spoken — at end of stream, not at its period.
func TestShortBeatRepliesOnceAtEndOfStream(t *testing.T) {
	provider := &groupingFakeTTS{}
	player := newFakePlayer(time.Millisecond)
	defer player.Close()
	p := &Pipeline{TTS: provider, Player: player, Events: events.NewBus()}

	chunks := make(chan string)
	done := make(chan brain.StreamResult, 1)
	go func() {
		defer close(chunks)
		defer close(done)
		chunks <- "Hm. "
		// Shorter than idleFlushAfter: nothing may reach TTS while the beat
		// could still get a follower.
		time.Sleep(100 * time.Millisecond)
		if got := provider.Texts(); len(got) != 0 {
			t.Errorf("Synthesize calls = %q, want the beat held for a follower", got)
		}
		done <- brain.StreamResult{}
	}()

	metrics := newTurnMetrics()
	turn := p.newTurnConductor(metrics)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, _, err := p.streamResponse(ctx, cancel, &brain.Stream{Chunks: chunks, Done: done}, false, metrics, turn, nil); err != nil {
		t.Fatalf("streamResponse: %v", err)
	}

	if got := provider.Texts(); len(got) != 1 || got[0] != "Hm." {
		t.Fatalf("Synthesize calls = %q, want one call with the beat", got)
	}
}

// The idle flush is what keeps a held beat from waiting out a tool call: a
// complete sentence goes out when the brain stops producing, and a partial one
// never does.
func TestIdleFlushSpeaksCompleteSentencesOnly(t *testing.T) {
	for _, tc := range []struct {
		name      string
		chunk     string
		wantCalls int
	}{
		{name: "complete sentence", chunk: "Hm. ", wantCalls: 1},
		{name: "partial sentence", chunk: "this line never ends", wantCalls: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provider := &groupingFakeTTS{}
			player := newFakePlayer(time.Millisecond)
			defer player.Close()
			p := &Pipeline{TTS: provider, Player: player, Events: events.NewBus()}

			chunks := make(chan string)
			done := make(chan brain.StreamResult, 1)
			idle := make(chan struct{})
			go func() {
				defer close(chunks)
				defer close(done)
				chunks <- tc.chunk
				// Tokens stop here. The timer is the only thing that can speak.
				time.Sleep(4 * idleFlushAfter)
				if got := len(provider.Texts()); got != tc.wantCalls {
					t.Errorf("Synthesize calls after the idle timer = %d, want %d", got, tc.wantCalls)
				}
				close(idle)
				done <- brain.StreamResult{}
			}()

			metrics := newTurnMetrics()
			turn := p.newTurnConductor(metrics)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if _, _, err := p.streamResponse(ctx, cancel, &brain.Stream{Chunks: chunks, Done: done}, false, metrics, turn, nil); err != nil {
				t.Fatalf("streamResponse: %v", err)
			}
			<-idle
		})
	}
}

// The timer and the follower the group is waiting for can become ready in the
// same round, and select has no priority. The loop must take the follower:
// flushing on the timer there is the two-contour defect with a race in front of
// it. Repeated because one run can take the right branch by luck.
func TestIdleFlushLosesToAFollowerAtTheDeadline(t *testing.T) {
	for i := 0; i < 8; i++ {
		provider := &groupingFakeTTS{joins: true}
		calls := speakTimed(t, provider, time.Nanosecond, 0, "Wait. Wait, you what? ")
		if len(calls) != 1 {
			t.Fatalf("run %d Synthesize calls = %q, want the follower merged", i, calls)
		}
	}
}

// A blank line is a boundary the listener hears, so it closes the group even
// under the floor — including the ordinary markdown paragraph, where the break
// falls between two sentences and the chunker used to trim it away.
func TestParagraphBreakEndsTheGroup(t *testing.T) {
	t.Run("between two sentences", func(t *testing.T) {
		provider := &groupingFakeTTS{joins: true}
		calls, response := runScript(t, provider, "Short line.\n\nOkay then. ")
		if len(calls) != 2 {
			t.Fatalf("Synthesize calls = %q, want the paragraph break to split them", calls)
		}
		if calls[0] != "Short line." || calls[1] != "Okay then." {
			t.Fatalf("Synthesize calls = %q, want one paragraph each", calls)
		}
		if response != "Short line. Okay then." {
			t.Fatalf("transcript = %q, want the mark cleaned away", response)
		}
	})
	t.Run("inside one segment", func(t *testing.T) {
		provider := &groupingFakeTTS{}
		calls := speakScript(t, provider, "Short line\n\nOkay then. ", "Second thing is here.")
		if len(calls) != 2 {
			t.Fatalf("Synthesize calls = %q, want the paragraph break to split them", calls)
		}
		if !strings.Contains(calls[0], "Okay then.") {
			t.Fatalf("first synthesis = %q, want the paragraph that ended", calls[0])
		}
	})
	t.Run("a single newline is not a break", func(t *testing.T) {
		provider := &groupingFakeTTS{joins: true}
		calls := speakScript(t, provider, "One line.\nTwo lines. ")
		if len(calls) != 1 {
			t.Fatalf("Synthesize calls = %q, want a wrapped line to stay one group", calls)
		}
	})
}

// The ramp lowers the size a group aims for below the floor, so a group does
// not outgrow the one playing while it synthesizes and leave the listener in
// the gap (RTF measurements in docs/benchmarks).
func TestSegmentRampLowersTheTargetBelowTheFloor(t *testing.T) {
	script := []string{
		"The build failed on the main branch today. ",
		"Alpha kept the deploy queue moving along without any trouble today. ",
		"Bravo kept the deploy queue moving along without any trouble today. ",
		"Delta kept the deploy queue moving along without any trouble today. ",
		"Gamma kept the deploy queue moving along without any trouble today. ",
		"Sigma kept the deploy queue moving along without any trouble today.",
	}
	// 42-char opening and 67-char sentences: 1.5 targets 63 chars, so one
	// sentence already clears it; 4.0 targets 168, lowered to the 80-char
	// floor, so the group waits for a second sentence.
	capped := speakScript(t, &groupingFakeTTS{ramp: 1.5}, script...)
	roomy := speakScript(t, &groupingFakeTTS{ramp: 4.0}, script...)

	if len(capped) < 2 || len(roomy) < 2 {
		t.Fatalf("want at least two groups each, got capped=%q roomy=%q", capped, roomy)
	}
	if len(capped[1]) >= laterFloorChars {
		t.Errorf("ramped group = %d chars (%q), want the ramp to cap it under the %d floor",
			len(capped[1]), capped[1], laterFloorChars)
	}
	if len(roomy[1]) < laterFloorChars {
		t.Errorf("unramped group = %d chars (%q), want the floor to decide", len(roomy[1]), roomy[1])
	}
	for i, call := range roomy {
		if len(call) > segmentCeilingChars {
			t.Errorf("group %d is %d chars, over the %d ceiling", i, len(call), segmentCeilingChars)
		}
	}
}

// A tag with no prose is not a segment of its own: it rides with the sentence
// after it, or with the one before it when the stream ends first.
//
// The provider here vocalizes tags and joins sentences, so the tag survives
// into the request text and a test can see which call it landed on.
func TestVocalTagOnlySegmentsRideWithSpeech(t *testing.T) {
	tagged := func() *groupingFakeTTS {
		return &groupingFakeTTS{fakeTTS: fakeTTS{vocalTags: true}, joins: true}
	}
	t.Run("attaches forward", func(t *testing.T) {
		provider := &groupingFakeTTS{}
		calls := speakScript(t, provider, "[laugh]. ", "The build finished successfully now.")
		if len(calls) != 1 {
			t.Fatalf("Synthesize calls = %q, want one call carrying both", calls)
		}
		if !strings.Contains(calls[0], "The build finished successfully now.") {
			t.Fatalf("synthesized %q, want the sentence spoken", calls[0])
		}
	})
	t.Run("waits out the idle timer for its sentence", func(t *testing.T) {
		provider := tagged()
		calls := speakTimed(t, provider, 25*time.Millisecond, 100*time.Millisecond,
			"[laugh]. ", "The build finished successfully now.")
		if len(calls) != 1 {
			t.Fatalf("Synthesize calls = %q, want the tag held past the idle timer", calls)
		}
		if !strings.Contains(calls[0], "[laugh]") || !strings.Contains(calls[0], "The build finished successfully now.") {
			t.Fatalf("synthesized %q, want the tag on the sentence it belongs to", calls[0])
		}
	})
	t.Run("an unknown bracket is not a tag-only beat", func(t *testing.T) {
		provider := tagged()
		calls := speakScript(t, provider, "[mouth] [laugh] hello there, the build is green.")
		if len(calls) != 1 {
			t.Fatalf("Synthesize calls = %q, want one call", calls)
		}
		for _, want := range []string{"[mouth]", "[laugh]", "hello there, the build is green."} {
			if !strings.Contains(calls[0], want) {
				t.Fatalf("synthesized %q, want it to carry %q", calls[0], want)
			}
		}
	})
	t.Run("a question beat carries the tag before it", func(t *testing.T) {
		for _, joins := range []bool{false, true} {
			provider := &groupingFakeTTS{fakeTTS: fakeTTS{vocalTags: true}, joins: joins}
			calls := speakScript(t, provider, "[laugh]. ", "Really?")
			if len(calls) != 1 {
				t.Fatalf("joins=%v Synthesize calls = %q, want the tag and the beat together", joins, calls)
			}
			if !strings.Contains(calls[0], "[laugh]") {
				t.Fatalf("joins=%v synthesized %q, want the tag carried", joins, calls[0])
			}
		}
	})
	t.Run("attaches back at end of stream", func(t *testing.T) {
		provider := &groupingFakeTTS{}
		calls := speakScript(t, provider, "Oh no. ", "[laugh]")
		if len(calls) != 1 {
			t.Fatalf("Synthesize calls = %q, want no call for the trailing tag alone", calls)
		}
	})
}

// A beat that ends in a question mark cannot be merged textually — the
// rewrite has nothing to turn the "?" into — so it goes out alone.
func TestQuestionBeatIsNotTextuallyJoined(t *testing.T) {
	textual := speakScript(t, &groupingFakeTTS{}, "Really? ", "You what?")
	if len(textual) != 2 {
		t.Fatalf("textual join calls = %q, want the question spoken on its own", textual)
	}
	if textual[0] != "Really?" {
		t.Fatalf("first synthesis = %q, want the question beat alone", textual[0])
	}

	request := speakScript(t, &groupingFakeTTS{joins: true}, "Really? ", "You what?")
	if len(request) != 1 {
		t.Fatalf("request join calls = %q, want one request over both", request)
	}
}

// A group of full sentences is grouped to save per-call overhead, not to be
// re-read as one sentence. Only a group the loop held a short beat for is
// rewritten, so two ordinary sentences keep their period even on an engine
// that re-splits them.
func TestGroupWithoutAShortBeatKeepsItsPunctuation(t *testing.T) {
	const (
		opening = "The release build finished on main a few minutes ago."
		second  = "Every unit test passed on the first attempt."
		third   = "The linter came back clean across the tree."
	)
	calls := speakScript(t, &groupingFakeTTS{}, opening+" ", second+" ", third)
	if len(calls) != 2 {
		t.Fatalf("Synthesize calls = %q, want the opening then the two grouped sentences", calls)
	}
	if want := second + " " + third; calls[1] != want {
		t.Fatalf("synthesized %q, want %q", calls[1], want)
	}
}

// The fallback re-prepares the group's text, so it must be given the same
// decision the primary synthesis was given: a group the loop joined stays one
// contour when Kokoro picks it up mid-turn, and a group it did not join keeps
// its sentence punctuation there too.
func TestFallbackCarriesTheLoopsJoinDecision(t *testing.T) {
	const group = "Wait. Wait, you what?"
	for _, tc := range []struct {
		name      string
		joinBeats bool
		want      string
	}{
		{name: "joined group stays joined", joinBeats: true, want: "Wait, wait, you what?"},
		{name: "unjoined group is spoken as written", joinBeats: false, want: group},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fallback := &groupingFakeTTS{}
			p := &Pipeline{TTS: &groupingFakeTTS{}, TTSFallback: fallback, Player: &fallbackBoundaryPlayer{}, Events: events.NewBus()}
			now := time.Now()
			outcome := p.synthesizeSegmentObserved(context.Background(), make(chan struct{}), synthWork{
				sentence: group, textReady: now, queuedAt: now, joinBeats: tc.joinBeats,
			}, 0, new(atomic.Bool), make(chan playbackEvent, 2), nil)
			if !outcome.enqueued {
				t.Fatalf("outcome = %+v, want the fallback playback enqueued", outcome)
			}
			if got := fallback.Texts(); len(got) != 1 || got[0] != tc.want {
				t.Fatalf("fallback spoke %q, want %q", got, tc.want)
			}
		})
	}
}

func TestJoinShortBeats(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		want string
	}{
		{name: "single word beat pulls its follower down", in: "Wait. Wait, you what?", want: "Wait, wait, you what?"},
		{name: "multi word beat keeps the follower capitalized", in: "Oh no. The build failed again.", want: "Oh no, The build failed again."},
		{name: "exclamation softens too", in: "Stop! You are early.", want: "Stop, you are early."},
		{name: "question beats are left alone", in: "Really? You what?", want: "Really? You what?"},
		{name: "sentences above the beat are spoken as written", in: "The build failed on main. It failed again.", want: "The build failed on main. It failed again."},
		{name: "abbreviations are not boundaries", in: "Dr. Smith is here.", want: "Dr. Smith is here."},
		{name: "a lone sentence is untouched", in: "Hm.", want: "Hm."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := joinShortBeats(tc.in); got != tc.want {
				t.Fatalf("joinShortBeats(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// The turn's shape is reported so a chunking change can be read off a run:
// how many segments, how long they were, and how many boundaries followed a
// segment too short to earn one.
func TestTurnMetricsReportSegmentShape(t *testing.T) {
	provider := &groupingFakeTTS{}
	player := newFakePlayer(time.Millisecond)
	defer player.Close()
	p := &Pipeline{TTS: provider, Player: player, Events: events.NewBus()}

	metrics := newTurnMetrics()
	turn := p.newTurnConductor(metrics)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	script := make([]string, 0, len(standaloneSentences))
	for _, sentence := range standaloneSentences {
		script = append(script, sentence+" ")
	}
	if _, _, err := p.streamResponse(ctx, cancel, scriptedStream(ctx, script, nil), false, metrics, turn, nil); err != nil {
		t.Fatalf("streamResponse: %v", err)
	}

	snap := metrics.snapshot()
	if snap.SegmentCount != len(snap.Segments) || snap.SegmentCount == 0 {
		t.Fatalf("SegmentCount = %d, Segments = %d", snap.SegmentCount, len(snap.Segments))
	}
	if snap.MedianSegmentChars != len(standaloneSentences[0]) {
		t.Errorf("MedianSegmentChars = %d, want %d", snap.MedianSegmentChars, len(standaloneSentences[0]))
	}
	// Every group here met the target it was flushed against — with a 1.0 ramp
	// that target is the previous group, not the 80-char floor the groups were
	// never allowed to reach. Counting against the floor reported a healthy
	// turn as all-short, which is the reading this metric exists to avoid.
	if snap.ShortBoundaries != 0 {
		t.Errorf("ShortBoundaries = %d, want 0: %+v", snap.ShortBoundaries, snap.Segments)
	}
}

// The opening holds while every sentence in it is still an interjection:
// "Wait. No." is not a finished thought, and three contours for it is the
// defect with one more beat in it.
func TestOpeningHoldsConsecutiveShortBeats(t *testing.T) {
	for _, joins := range []bool{false, true} {
		provider := &groupingFakeTTS{joins: joins}
		calls := speakScript(t, provider, "Wait. ", "No. ", "You what?")
		if len(calls) != 1 {
			t.Fatalf("joins=%v Synthesize calls = %q, want one call for one thought", joins, calls)
		}
	}
}

// The ceiling is the veto: a held group plus a follower it cannot fit goes out
// first, so the player never has to wait out a whole paragraph as one call.
func TestGroupFlushesBeforeItWouldOutgrowTheCeiling(t *testing.T) {
	// 215 chars: alone it fits under the ceiling, behind the held beat it does
	// not.
	const huge = "The deploy pipeline rebuilt every container image from scratch because the cache key changed when the base image was bumped last night, and the registry mirror had to repopulate every layer before the rollout began."
	provider := &groupingFakeTTS{ramp: 1.5, joins: true}
	calls := speakScript(t, provider,
		"The build failed on the main branch today. ",
		"Wait. ",
		huge+" ",
		"Sigma is another line.",
	)
	for i, call := range calls {
		if strings.Contains(call, "Wait.") && strings.Contains(call, huge) {
			t.Fatalf("call %d = %q, want the held beat flushed before the follower it cannot fit", i, call)
		}
		if speakableChars(call) > segmentCeilingChars {
			t.Errorf("call %d is %d chars, over the %d ceiling", i, speakableChars(call), segmentCeilingChars)
		}
	}
}

// One sentence longer than the ceiling still goes alone: the veto never fires
// on an empty group, because splitting a sentence is not a thing grouping does.
func TestASentenceOverTheCeilingGoesAlone(t *testing.T) {
	const over = "The deploy pipeline rebuilt every container image from scratch because the cache key changed when the base image was bumped last night, and the registry mirror had to repopulate every layer before the rollout could begin again this morning."
	calls := speakScript(t, &groupingFakeTTS{ramp: 1.5, joins: true},
		"The build failed on the main branch today. ", over)
	if len(calls) != 2 || calls[1] != over {
		t.Fatalf("Synthesize calls = %q, want the long sentence as its own call", calls)
	}
}

// Grouping has to grow. The ramp used to veto the append as well as set the
// target, so a group could neither flush (still under the target) nor take its
// follower (already over the veto): after a short opening every later group was
// one sentence, and each short group ratcheted the next one shorter.
func TestGroupsGrowAfterAShortOpening(t *testing.T) {
	// A 42-char opening and six 41-char sentences: at ramp 1.5 the first later
	// group targets 63 chars, which one sentence cannot reach.
	script := []string{
		"The build failed on the main branch today. ",
		"Alpha kept the deploy queue moving along. ",
		"Bravo kept the deploy queue moving along. ",
		"Delta kept the deploy queue moving along. ",
		"Gamma kept the deploy queue moving along. ",
		"Sigma kept the deploy queue moving along. ",
		"Omega kept the deploy queue moving along.",
	}
	for _, ramp := range []float64{1.0, 1.5} {
		t.Run(fmt.Sprintf("ramp %.1f", ramp), func(t *testing.T) {
			calls := speakScript(t, &groupingFakeTTS{ramp: ramp}, script...)
			if len(calls) > 4 {
				t.Fatalf("Synthesize calls = %d %q, want at most 4: the groups never grew", len(calls), calls)
			}
			if len(calls) < 2 {
				t.Fatalf("Synthesize calls = %q, want the opening and at least one group after it", calls)
			}
			if got := len(splitSentences(calls[1])); got != 2 {
				t.Errorf("second group = %d sentences (%q), want 2", got, calls[1])
			}
			// The ramp lowers the target a group aims for; it does not cap the
			// sentence that crosses it, so a later group still reaches the
			// size of the one before it.
			if speakableChars(calls[1]) < speakableChars(calls[0]) {
				t.Errorf("second group = %d chars, want at least the opening's %d",
					speakableChars(calls[1]), speakableChars(calls[0]))
			}
		})
	}
}

// The veto also split a held beat from the sentence it belongs to: under the
// ramp "Wait." plus a long follower was over the limit, so the beat went out
// alone — the two-contour defect the merge exists to prevent.
func TestHeldBeatRidesWithALongFollower(t *testing.T) {
	const long = "The deploy pipeline rebuilt every container image from scratch because the cache key changed when the base image was bumped."
	for _, ramp := range []float64{1.0, 1.5} {
		t.Run(fmt.Sprintf("ramp %.1f", ramp), func(t *testing.T) {
			calls := speakScript(t, &groupingFakeTTS{ramp: ramp, joins: true},
				"The build failed on the main branch today. ", "Wait. ", long)
			if len(calls) != 2 {
				t.Fatalf("Synthesize calls = %q, want the opening then the beat with its follower", calls)
			}
			if !strings.Contains(calls[1], "Wait.") || !strings.Contains(calls[1], long) {
				t.Fatalf("second call = %q, want the beat and its sentence in one", calls[1])
			}
		})
	}
}

// A paragraph break used to bypass the tag-only rule: "[laugh]." before a blank
// line was flushed as its own synthesis, which is a tag spoken alone.
func TestParagraphBreakDoesNotStrandATagOnlyTail(t *testing.T) {
	provider := &groupingFakeTTS{fakeTTS: fakeTTS{vocalTags: true}, joins: true}
	calls := speakScript(t, provider, "[laugh].\n\nThe build finished successfully now.")
	if len(calls) != 1 {
		t.Fatalf("Synthesize calls = %q, want the tag carried into the new paragraph", calls)
	}
	if !strings.Contains(calls[0], "[laugh]") || !strings.Contains(calls[0], "The build finished successfully now.") {
		t.Fatalf("synthesized %q, want the tag on the sentence it belongs to", calls[0])
	}
}

// The idle branch takes a sentence at the deadline, and that take can route to
// nothing — a duplicate the turn already spoke, a gated line, a muted turn.
// The timer it consumed has to be re-armed there, or the held group waits for
// the next token or end of stream instead of the deadline it had reached.
// Repeated because a run can take the follower through the loop's main sentence
// case and never reach the branch.
func TestIdleFlushRearmsWhenTheTakeRoutesToNothing(t *testing.T) {
	const opening = "The build failed on the main branch today. "
	for i := range 8 {
		t.Run(fmt.Sprintf("run %d", i), func(t *testing.T) {
			provider := &groupingFakeTTS{joins: true}
			player := newFakePlayer(time.Millisecond)
			defer player.Close()
			p := &Pipeline{TTS: provider, Player: player, Events: events.NewBus(), idleFlush: time.Nanosecond}

			in := make(chan string)
			done := make(chan brain.StreamResult, 1)
			var spoke bool
			go func() {
				defer close(in)
				defer close(done)
				in <- opening
				// The duplicate rides in the beat's chunk so it is the segment
				// waiting when the idle deadline arrives, and admission drops
				// it. Then the brain goes quiet: only the timer can speak.
				in <- "Wait. " + opening
				deadline := time.Now().Add(500 * time.Millisecond)
				for time.Now().Before(deadline) {
					if len(provider.Texts()) > 1 {
						spoke = true
						break
					}
					time.Sleep(time.Millisecond)
				}
				done <- brain.StreamResult{}
			}()

			metrics := newTurnMetrics()
			turn := p.newTurnConductor(metrics)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if _, _, err := p.streamResponse(ctx, cancel, &brain.Stream{Chunks: in, Done: done}, false, metrics, turn, nil); err != nil {
				t.Fatalf("streamResponse: %v", err)
			}
			if !spoke {
				t.Fatalf("the held beat waited for end of stream, not the idle timer: %q", provider.Texts())
			}
		})
	}
}

// Grouping follows the engine that speaks. A Qwen-like primary that fails onto
// a Kokoro-like fallback must stop holding "?" beats for a request join the
// fallback re-splits anyway.
func TestGroupingFollowsTheProviderThatSpeaks(t *testing.T) {
	fallback := &groupingFakeTTS{}
	player := newFakePlayer(time.Millisecond)
	defer player.Close()
	bus := events.NewBus()
	spoke := make(chan struct{}, 4)
	events.Subscribe(bus, func(events.SpeakingStarted) {
		select {
		case spoke <- struct{}{}:
		default:
		}
	})
	p := &Pipeline{TTS: joinErrorTTS{}, TTSFallback: fallback, Player: player, Events: bus}

	chunks := make(chan string)
	done := make(chan brain.StreamResult, 1)
	go func() {
		defer close(chunks)
		defer close(done)
		chunks <- "The build failed on main. "
		select {
		case <-spoke:
		case <-time.After(3 * time.Second):
			t.Error("the fallback never spoke")
			return
		}
		chunks <- "Really? "
		chunks <- "You what? "
		done <- brain.StreamResult{}
	}()

	metrics := newTurnMetrics()
	turn := p.newTurnConductor(metrics)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, _, err := p.streamResponse(ctx, cancel, &brain.Stream{Chunks: chunks, Done: done}, false, metrics, turn, nil); err != nil {
		t.Fatalf("streamResponse: %v", err)
	}

	calls := fallback.Texts()
	if len(calls) != 3 {
		t.Fatalf("fallback Synthesize calls = %q, want the two beats spoken separately", calls)
	}
	if calls[1] != "Really?" {
		t.Fatalf("second fallback call = %q, want the question beat alone", calls[1])
	}
}

// joinErrorTTS is a Qwen-like primary that always fails, so the turn latches
// onto the fallback on its first segment.
type joinErrorTTS struct{}

func (joinErrorTTS) Synthesize(context.Context, string) (*audio.PCMStream, error) {
	return nil, &tts.ProviderError{Provider: "qwen3-tts", Kind: tts.ProviderErrorUnavailable, Err: errors.New("worker unavailable")}
}
func (joinErrorTTS) Available() bool                       { return true }
func (joinErrorTTS) ListVoices(string, string) []tts.Voice { return nil }
func (joinErrorTTS) JoinsSentences() bool                  { return true }
