package pipeline

import (
	"strings"
	"unicode"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
)

const voiceQueueDepth = 2

// How many speakable segments go into one synthesis call.
//
// laterBatchSegments is 1, and the 2 it was briefly set to is why this comment
// exists. Batching two sentences per call was supposed to let the synthesiser
// carry intonation across a sentence boundary. Measurement killed it:
//
//	Kokoro realtime factor         1.32x   (0.901 s to synthesize 1.185 s of audio)
//	synth(2 sentences)             1.989 s
//	playback(1 opening sentence)   1.185 s
//	=> 804 ms of silence after the opening sentence
//
// The synthesis worker is single and ordered, so segment 2 is synthesized while
// segment 1 plays. At 1.32x, a two-sentence segment 2 cannot finish in time and
// the listener hears a gap in the most audible position in the reply. Meanwhile
// the prosody benefit was unverifiable: merged audio measurably differs from
// concatenated audio, but total duration, inter-sentence pause and onset F0 were
// identical, so nothing showed the difference was an improvement.
//
// Batching becomes free above a realtime factor of about 2.3x — roughly 1.8x
// faster than Kokoro is today. `go test -tags integration ./pkg/voiceagent/tts/ -run
// TestKokoroRealtimeFactor` is the measurement; phase 009's TTS bake-off should
// re-run it, and a candidate that clears 2.3x makes this a one-constant change.
//
// See festival decision D009.
const (
	openingBatchSegments = 1
	laterBatchSegments   = 1
)

// speakableSegment reports whether a cleaned segment earns a place in the
// transcript and the synthesis queue. Call this. Neither half below is
// sufficient alone, and the two reject disjoint kinds of junk:
//
//	"Hmm."     speakable=false  pronounceable=true   (a bare hesitation)
//	"\u200b"  speakable=true   pronounceable=false  (a zero-width space)
//	"\u0301"  speakable=true   pronounceable=false  (a lone combining accent)
//
// So the pair is not redundant in either direction, which is easy to assume
// from a glance and wrong. TestSpeakableSegmentNeedsBothHalves fails if either
// half is dropped.
//
// Both speech paths decide here: streamResponse per segment as segments arrive,
// RunTurnTextMode through speakableFromResponse. Reaching for a half instead is
// how a filler-only reply got spoken while the recovery line went only to
// history (PR #223 review).
func speakableSegment(s string) bool {
	return brain.HasSpeakableContent(s) && hasPronounceableContent(s)
}

// speakableFromResponse reduces a whole gated reply to the text worth
// synthesizing, applying per segment what the progressive loop applies as
// segments arrive: the same splitter, the same cleaning, the same predicate.
//
// Text mode used to synthesize the gated reply verbatim, so it spoke markdown
// ("Here is **bold** text") and read a leading "Hmm." aloud before the recovery
// line that had replaced it — the PR #223 bug living on in the one path that
// fix did not reach.
//
// Segment granularity is the whole point. speakableSegment on the entire reply
// cannot work: "Hmm. <recovery>" satisfies both halves as soon as the recovery
// text is present, so the filler rides along. Only splitting first exposes it.
func speakableFromResponse(gated string) string {
	if gated == "" {
		return ""
	}
	input := make(chan string, 1)
	input <- gated
	close(input)

	var kept []string
	seen := make(map[string]struct{})
	for segment := range brain.ChunkSentencesRaw(input) {
		if cleaned := brain.CleanForVoice(segment); speakableSegment(cleaned) {
			if _, dup := seen[cleaned]; dup {
				continue
			}
			seen[cleaned] = struct{}{}
			kept = append(kept, cleaned)
		}
	}
	return strings.Join(kept, " ")
}

// hasPronounceableContent reports whether a segment contains anything a
// synthesiser can say. Punctuation left behind by cleaning — a bare "." from
// "Umm." — is not speech, and must not reach TTS or be batched onto a real
// sentence. It also rejects a segment of nothing but invisible runes, which
// brain.HasSpeakableContent accepts: it trims spaces, punctuation and symbols,
// and a combining mark is none of those.
//
// Not called directly — speakableSegment is the entry point.
func hasPronounceableContent(s string) bool {
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return true
		}
	}
	return false
}
