package pipeline

import (
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/textclean"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/tts"
)

const voiceQueueDepth = 2

// Segment grouping: how much text goes into one synthesis call.
//
// A group is whole sentences. The provider's SegmentRamp lowers the size a
// group aims for, because a group that outgrows the one playing while it
// synthesizes is heard as a gap (RTF measurements in docs/benchmarks). The
// ramp is a target and nothing more; the ceiling is the only hard bound.
const (
	// shortBeatChars is the length below which a sentence is an interjection.
	// "Wait." and "Hm." are one thought with whatever follows them, and a
	// synthesis call each is two contours where the listener expects one.
	shortBeatChars = 25
	// laterFloorChars is how much text a group after the opening wants before
	// it goes out. The opening stays short because time-to-first-audio waits
	// on it; after that the listener is not waiting and a boundary is not free.
	laterFloorChars = 80
	// segmentCeilingChars bounds a group so one long paragraph cannot become a
	// single synthesis the player has to wait out.
	segmentCeilingChars = 220
	// idleFlushAfter is how long a complete buffered sentence waits for a
	// follower once brain tokens stop arriving.
	idleFlushAfter = 350 * time.Millisecond
)

// segmentGrouping is the flush policy for one turn: the provider's ramp, and
// whether merging sentences means one request or one rewritten sentence.
type segmentGrouping struct {
	ramp float64
	// textualJoin is set for a provider that re-splits sentence punctuation
	// inside a call (Kokoro). There a beat ending in "?" cannot be merged, so
	// it flushes alone rather than waiting for a follower it cannot join.
	textualJoin bool
}

func groupingFor(provider tts.Provider) segmentGrouping {
	return segmentGrouping{ramp: tts.SegmentRampOf(provider), textualJoin: !tts.JoinsSentencesOf(provider)}
}

// ready reports whether the buffered group should be synthesized now. group is
// the complete sentences admitted so far, paragraphBreak marks a blank line
// after the last of them, and lastChars is the size of the group before this
// one (0 while the opening is still buffered). forced is the idle timer (D4):
// a group that has waited out the brain goes even though it is under target.
func (g segmentGrouping) ready(group []string, paragraphBreak bool, lastChars int, forced bool) bool {
	if len(group) == 0 {
		return false
	}
	// A tag with no prose ([gasp]) never closes a group: it rides with the
	// next sentence, or with the group before it at end of stream. The idle
	// timer does not override this — a tag spoken alone is the defect.
	if heldTagTail(group) {
		return false
	}
	if forced || paragraphBreak {
		return true
	}
	if allShortBeats(group) {
		// The interjection merge. Beats wait for a sentence that is not one,
		// the idle timer or end of stream — unless the merge would have to be
		// textual and the last beat ends in "?", which no rewrite can carry
		// into the next sentence.
		return g.textualJoin && endsInQuestion(group[len(group)-1])
	}
	if lastChars == 0 {
		// Opening: closed by the first admitted sentence that is not a beat.
		return true
	}
	return speakableChars(strings.Join(group, " ")) >= g.flushTarget(lastChars)
}

// flushTarget is the size ready() wants before a group goes out: the floor,
// lowered to the provider's ramp. It is a target and nothing else — the
// sentence that crosses it is spoken with the group, so a group may overshoot
// by one sentence. The opening has no ramp to measure against and is only held
// while it is an interjection, so its target is the beat threshold.
func (g segmentGrouping) flushTarget(lastChars int) int {
	if lastChars == 0 {
		return shortBeatChars
	}
	target := laterFloorChars
	if g.ramp > 0 {
		if ramped := int(g.ramp * float64(lastChars)); ramped < target {
			target = ramped
		}
	}
	return target
}

// exceedsCeiling reports that appending next would push the held group past
// segmentCeilingChars, so the group goes out first and next opens a new one.
//
// The ceiling is the only veto. Vetoing on the ramp as well left a group that
// could neither flush (still under the target) nor grow (already over the
// veto), so after a short opening every later group was one sentence and the
// ratchet never let them recover. It also split a held beat from the sentence
// it belongs to; under the ceiling alone, "Wait." plus a 120-char follower is
// one call. An empty group is never vetoed — a single sentence over the
// ceiling still goes alone — and neither is one ending in a tag-only beat,
// which has no prose of its own and has to ride forward.
func exceedsCeiling(group []string, next string) bool {
	if len(group) == 0 || heldTagTail(group) {
		return false
	}
	return speakableChars(strings.Join(group, " "))+1+speakableChars(next) > segmentCeilingChars
}

// heldTagTail reports a group whose last admitted segment is a tag with no
// prose. Nothing may flush the group out from under it — not the ceiling, not
// a paragraph break, not the idle timer: the tag rides into the next sentence,
// or into the group before it at end of stream.
func heldTagTail(group []string) bool {
	return len(group) > 0 && tagOnlySegment(group[len(group)-1])
}

// allShortBeats reports a group that is still nothing but interjections.
// "Wait. No." is not a finished opening, so it keeps waiting for the sentence
// those beats belong to.
func allShortBeats(group []string) bool {
	for _, sentence := range group {
		if speakableChars(sentence) >= shortBeatChars {
			return false
		}
	}
	return true
}

// speakableChars is a segment's length as the listener hears it: vocal tags
// contribute no speech, and every grouping threshold is about audio duration.
func speakableChars(s string) int {
	return len(strings.TrimSpace(stripVocalTags(s)))
}

// tagOnlySegment reports a segment admitted only because it carries a sound —
// SpeechProjection.admit lets these through with no speakable prose (see
// speech_projection.go).
func tagOnlySegment(s string) bool {
	return hasVocalTag(s) && !speakableSegment(stripVocalTags(s))
}

// endsInQuestion reports whether a segment's last spoken character is "?".
// Closing quotes ride with the sentence, so they are trimmed first.
func endsInQuestion(s string) bool {
	trimmed := strings.TrimRightFunc(strings.TrimSpace(stripVocalTags(s)), textclean.IsSpeechQuote)
	return strings.HasSuffix(trimmed, "?")
}

// joinShortBeats merges a group's interjections into the sentence that follows
// them, for an engine that re-splits sentence punctuation inside one call.
// sherpa's Kokoro frontend starts a new entry at . ! ? ; and runs the model
// once per entry, so "Wait. Wait, you what?" is two contours however it is
// requested; with the beat's period rewritten to a comma it is one.
//
// Only a beat under shortBeatChars is rewritten. Longer sentences are grouped
// to save per-call overhead, not to change how they are read, so they are
// spoken as written.
func joinShortBeats(group string) string {
	sentences := splitSentences(group)
	if len(sentences) < 2 {
		return group
	}
	for i := 0; i < len(sentences)-1; i++ {
		beat := sentences[i]
		if speakableChars(beat) >= shortBeatChars || endsInQuestion(beat) {
			continue
		}
		if !hasPronounceableContent(beat) {
			continue
		}
		softened, ok := softenBeatEnd(beat)
		if !ok {
			continue
		}
		sentences[i] = softened
		if singleWordBeat(beat) {
			// "Wait, wait, you what?" reads as one phrase; "Oh no, The build
			// failed." does not, so only a one-word beat pulls the follower
			// down with it.
			sentences[i+1] = lowerFirstLetter(sentences[i+1])
		}
	}
	return strings.Join(sentences, " ")
}

// splitSentences re-reads a group as the sentences it was assembled from,
// through the same segmenter that produced them so "Dr. Smith" stays one.
func splitSentences(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	input := make(chan string, 1)
	input <- s
	close(input)
	var out []string
	for segment := range brain.ChunkSentencesRaw(input) {
		out = append(out, segment)
	}
	return out
}

// softenBeatEnd turns a beat's terminal "." or "!" into a comma, keeping any
// closing quote that rode with it. Reports false when the beat ends in
// something else, which is left alone.
func softenBeatEnd(s string) (string, bool) {
	trimmed := strings.TrimRight(s, " ")
	end := len(trimmed)
	for end > 0 {
		r, size := utf8.DecodeLastRuneInString(trimmed[:end])
		if !textclean.IsSpeechQuote(r) {
			break
		}
		end -= size
	}
	if end == 0 {
		return s, false
	}
	r, size := utf8.DecodeLastRuneInString(trimmed[:end])
	if r != '.' && r != '!' {
		return s, false
	}
	return trimmed[:end-size] + "," + trimmed[end:], true
}

func singleWordBeat(s string) bool {
	return len(strings.Fields(strings.TrimSpace(stripVocalTags(s)))) == 1
}

func lowerFirstLetter(s string) string {
	for i, r := range s {
		if !unicode.IsLetter(r) {
			continue
		}
		if unicode.IsLower(r) {
			return s
		}
		return s[:i] + string(unicode.ToLower(r)) + s[i+utf8.RuneLen(r):]
	}
	return s
}

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

// idleFlushDelay is how long a buffered group waits for a follower once brain
// tokens stop arriving.
func (p *Pipeline) idleFlushDelay() time.Duration {
	if p != nil && p.idleFlush > 0 {
		return p.idleFlush
	}
	return idleFlushAfter
}
