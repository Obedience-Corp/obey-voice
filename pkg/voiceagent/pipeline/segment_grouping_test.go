package pipeline

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
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
	player := newFakePlayer(time.Millisecond)
	defer player.Close()
	p := &Pipeline{TTS: provider, Player: player, Events: events.NewBus()}

	metrics := newTurnMetrics()
	turn := p.newTurnConductor(metrics)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if _, _, err := p.streamResponse(ctx, cancel, scriptedStream(ctx, chunks, nil), false, metrics, turn, nil); err != nil {
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

// A blank line is a boundary the listener hears, so it closes the group even
// under the floor.
func TestParagraphBreakEndsTheGroup(t *testing.T) {
	provider := &groupingFakeTTS{}
	calls := speakScript(t, provider, "Short line\n\nOkay then. ", "Second thing is here.")
	if len(calls) != 2 {
		t.Fatalf("Synthesize calls = %q, want the paragraph break to split them", calls)
	}
	if !strings.Contains(calls[0], "Okay then.") {
		t.Fatalf("first synthesis = %q, want the paragraph that ended", calls[0])
	}
}

// The ramp caps how much longer one group may be than the one playing while it
// synthesizes. Without it a group grows to the floor and the listener hears
// the gap — the 804 ms that reverted two-sentence batching.
func TestSegmentRampCapsGrowthBelowTheFloor(t *testing.T) {
	script := []string{
		"The build failed on the main branch today. ",
		"Alpha is another line. ", "Bravo is another line. ", "Delta is another line. ",
		"Gamma is another line. ", "Sigma is another line.",
	}
	// 42-char opening: 1.5 caps the next group at 63 chars, under the 80-char
	// floor; 4.0 caps it at 168, so the floor decides instead.
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
func TestVocalTagOnlySegmentsRideWithSpeech(t *testing.T) {
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
	// These sentences are each under the floor, so every boundary after the
	// first segment is a short one — the reading the metric exists to make
	// visible when a policy change stops merging.
	if want := snap.SegmentCount - 1; snap.ShortBoundaries != want {
		t.Errorf("ShortBoundaries = %d, want %d", snap.ShortBoundaries, want)
	}
}
