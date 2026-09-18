package pipeline

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

// speechFor runs one turn and returns everything TTS was asked to say, joined
// the way a listener would hear it. runner picks the path under test.
func speechFor(t *testing.T, chunks []string, textMode bool) string {
	t.Helper()
	bus := events.NewBus()
	ttsProvider := &fakeTTS{}
	player := newFakePlayer(time.Millisecond)
	defer player.Close()

	p := &Pipeline{
		STT:    &fakeSTT{text: "hello"},
		Brain:  &fakeBrain{chunks: chunks},
		TTS:    ttsProvider,
		Player: player,
		Events: bus,
	}
	if textMode {
		if err := p.RunTurnTextMode(context.Background(), "hello"); err != nil {
			t.Fatalf("RunTurnTextMode() error = %v", err)
		}
	} else if _, err := p.RunTurn(context.Background()); err != nil {
		t.Fatalf("RunTurn() error = %v", err)
	}
	return strings.Join(ttsProvider.Texts(), " ")
}

// TestTextModeSpeaksTheSameAsProgressive is the point of speakableFromResponse.
// A typed turn and a spoken turn are the same conversation, so they must not
// say different things — before this, text mode synthesized the gated reply
// verbatim and spoke markdown and fillers that voice mode had already dropped.
func TestTextModeSpeaksTheSameAsProgressive(t *testing.T) {
	cases := []struct {
		name   string
		chunks []string
	}{
		{"filler only, replaced by recovery", []string{"Hmm.", " Hmm, I lost my train of thought for a second. What were you saying?"}},
		{"markdown and a stripped filler", []string{"Here is **bold** text. Umm, and a second thought."}},
		{"plain reply", []string{"The capital of France is Paris. It has about two million people."}},
		{"voiced filler leading real content", []string{"Hmm, that is a good question. The answer is yes."}},
		// The marker rule runs segment by segment on one path and over the
		// whole reply on the other, so a list is where they drift apart.
		{"a numbered list", []string{"1. Wake up at six. 2. Stretch. 3. Coffee."}},
		// A list whose items repeat: the drop has to run before the duplicate
		// check on both paths, or text mode keeps the second marker after its
		// item is dropped as a repeat and speaks a number alone.
		{"a numbered list that repeats itself", []string{"1. Yes. 2. Yes."}},
		// Two markers in a row are one answer, and nothing follows them.
		{"two trailing markers", []string{"The codes are. 1. 2."}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			progressive := speechFor(t, tc.chunks, false)
			typed := speechFor(t, tc.chunks, true)
			if !saysTheSame(progressive, typed) {
				t.Errorf("paths disagree:\n  progressive: %q\n  text mode:   %q", progressive, typed)
			}
		})
	}
}

// saysTheSame compares two spoken strings up to the beat join and nothing
// else. On an engine that re-splits sentence punctuation the progressive path
// may merge a held short beat into the sentence it belongs to — a group only
// the turn loop can assemble, and one text mode never has, since it
// synthesizes the whole reply as a single string. So the one allowance is
// exactly that rewrite, applied to the typed text; every other difference,
// markdown, fillers and markers included, still fails the comparison.
func saysTheSame(progressive, typed string) bool {
	return progressive == typed || progressive == joinShortBeats(typed)
}

// TestTextModeDropsFillerOnlyAndMarkdown pins the two concrete regressions,
// so a parity break cannot be "fixed" by making both paths wrong together.
func TestTextModeDropsFillerOnlyAndMarkdown(t *testing.T) {
	t.Run("filler is not spoken before the recovery line", func(t *testing.T) {
		got := speechFor(t, []string{"Hmm.", " Hmm, I lost my train of thought for a second. What were you saying?"}, true)
		if strings.HasPrefix(got, "Hmm.") {
			t.Errorf("text mode still speaks the bare hesitation: %q", got)
		}
		if want := "Hmm, I lost my train of thought for a second. What were you saying?"; got != want {
			t.Errorf("spoke %q, want %q", got, want)
		}
	})

	t.Run("markdown does not reach TTS", func(t *testing.T) {
		got := speechFor(t, []string{"Here is **bold** text. Umm, and a second thought."}, true)
		if strings.Contains(got, "**") {
			t.Errorf("text mode still speaks markdown: %q", got)
		}
	})
}

// TestTextModeSpeaksSentencePunctuationAsWritten is the heard defect: text
// mode synthesizes the whole reply as one string, and the beat join was being
// applied to every string a non-joining engine (Kokoro) was handed. A reply
// with a short sentence in it came back as run-on speech — "It passed," in
// place of "It passed." — with nothing the turn loop had actually grouped.
func TestTextModeSpeaksSentencePunctuationAsWritten(t *testing.T) {
	const reply = "The build finished on main. It passed. Tests are green across the board. Nice."

	got := speechFor(t, []string{reply}, true)
	if got != reply {
		t.Fatalf("text mode spoke %q, want the reply as written %q", got, reply)
	}
	if strings.Contains(got, "passed,") {
		t.Errorf("a short sentence was merged into its follower: %q", got)
	}
}

// TestSpeakableFromResponseNeedsSegmentGranularity guards the shortcut a future
// reader will reach for: calling speakableSegment on the whole reply instead of
// per segment. That looks equivalent and is not — the AND passes as soon as any
// real content is present, so the filler rides along on the same string.
func TestSpeakableFromResponseNeedsSegmentGranularity(t *testing.T) {
	const glued = "Hmm. Hmm, I lost my train of thought for a second. What were you saying?"

	if !speakableSegment(glued) {
		t.Fatal("precondition failed: the whole-reply predicate is expected to accept this string")
	}
	got := speakableFromResponse(glued)
	if got == glued {
		t.Errorf("speakableFromResponse returned the reply unchanged — segment filtering is not happening: %q", got)
	}
	if strings.HasPrefix(got, "Hmm.") {
		t.Errorf("leading filler survived segmentation: %q", got)
	}
}

// The deferred marker rule, applied to a whole reply instead of segment by
// segment: a marker another sentence follows is a list's numbering, a marker
// nothing follows is the answer, and initials are neither.
func TestTextModeDefersTheMarkerDecision(t *testing.T) {
	for _, tc := range []struct{ name, reply, want string }{
		{"a trailing number is the answer", "Let me check. 4.", "Let me check. 4."},
		{"a trailing count is the answer", "The count came back. 100.", "The count came back. 100."},
		{"a lone number is the whole reply", "4.", "4."},
		{"initials are not markers", "His name is long. J. R. Tolkien wrote it.", "His name is long. J. R. Tolkien wrote it."},
		{"list numbering is dropped", "1. Wake up at six. 2. Stretch. 3. Coffee.", "Wake up at six. Stretch. Coffee."},
		{"numbering after a lead-in is dropped", "Here is my routine. 1. Wake up at six. 2. Stretch.", "Here is my routine. Wake up at six. Stretch."},
		{"a repeated item still drops its marker", "1. Yes. 2. Yes.", "Yes."},
		{"two trailing markers are one answer", "The codes are. 1. 2.", "The codes are. 1. 2."},
		{"two markers speech follows are numbering", "Here is the list. 1. 2. Coffee.", "Here is the list. Coffee."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := speakableFromResponse(tc.reply); got != tc.want {
				t.Errorf("speakableFromResponse(%q) = %q, want %q", tc.reply, got, tc.want)
			}
		})
	}
}

func TestSpeakableFromResponseEmptyInput(t *testing.T) {
	for _, in := range []string{"", "   ", ".", "Hmm.", "\u200b"} {
		if got := speakableFromResponse(in); got != "" {
			t.Errorf("speakableFromResponse(%q) = %q, want empty", in, got)
		}
	}
}
