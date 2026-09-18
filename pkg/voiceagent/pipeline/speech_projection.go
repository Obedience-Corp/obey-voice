package pipeline

import (
	"strings"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/textclean"
)

// SpokenSegment is what one raw model segment became on its way to the
// synthesizer.
type SpokenSegment struct {
	// Filtered is the segment with reasoning removed: the text the transcript
	// is built from, before gating and before cleaning. A think tag surviving
	// here is the leak #64/#68 closed, reopening — which is why it is reported
	// separately from Speakable, whose cleaning would erase the evidence.
	Filtered string
	// Speakable is the exact text queued for synthesis, and is empty when
	// nothing of the segment is heard: gated away as tool output or a fenced
	// block, cleaned to nothing, or a line this turn already spoke.
	Speakable string
	// Suppressed counts the lines the voice gate removed.
	Suppressed int
}

// Heard reports whether any part of the segment reaches the listener.
func (s SpokenSegment) Heard() bool { return s.Speakable != "" }

// SpeechProjection is the turn's path from raw model segments to the words a
// listener hears: think filter, voice gate, cleaning, the speakability
// predicate, and this turn's repeat suppression — in that order, with that
// state.
//
// streamResponse owns one per turn and calls the unexported halves directly,
// because it has to put the transcript and the mute check between gating and
// admission. Speak composes the same halves for callers that have neither.
//
// Exported for measurement. scripts/brain_bench times the first segment this
// admits, and a benchmark that re-implements the path measures its own copy of
// the product instead of the product: the first draft of that harness applied
// ThinkFilter and cleaning but not the gate, so a reply opening on a tool
// region or a fenced block would have been timed as a spoken word the pipeline
// suppresses (PR #71 review).
//
// Keep one per turn. Every stage is stateful — the gate's regions and the think
// filter's held prefix span segments, and repeat suppression is turn-scoped.
type SpeechProjection struct {
	think  textclean.ThinkFilter
	gate   voiceGate
	spoken map[string]struct{}
	// admitted marks that this turn has already sent speech to the queue. A
	// bare "4." is the answer when it is the reply and a list marker when it
	// follows one, and nothing in the segment itself tells them apart.
	admitted bool
}

// NewSpeechProjection starts a projection for one turn.
func NewSpeechProjection() *SpeechProjection {
	return &SpeechProjection{spoken: make(map[string]struct{})}
}

// Speak projects one raw segment onto the text handed to TTS.
//
// Feed every segment of the turn, in order, including ones that will not be
// spoken: a skipped segment loses the boundary that closes a tool region or a
// fence.
func (p *SpeechProjection) Speak(segment string) SpokenSegment {
	return p.project(p.filterThink(segment))
}

// Flush releases what the think filter still holds at end of stream and judges
// it by the same rules, the way the turn loop's final routeSegment does.
func (p *SpeechProjection) Flush() SpokenSegment {
	return p.project(p.flushThink())
}

func (p *SpeechProjection) project(text string) SpokenSegment {
	seg := SpokenSegment{Filtered: text}
	if text == "" {
		return seg
	}
	voiceText, stripped := p.gateSegment(text)
	seg.Suppressed = stripped
	if speakable, status := p.admit(voiceText); status == speechAdmitted {
		seg.Speakable = speakable
	}
	return seg
}

// filterThink removes reasoning from one raw segment. Blank segments are not
// fed to the filter at all, matching the turn loop.
func (p *SpeechProjection) filterThink(segment string) string {
	segment = strings.TrimSpace(segment)
	if segment == "" {
		return ""
	}
	return p.think.Feed(segment)
}

func (p *SpeechProjection) flushThink() string { return p.think.Flush() }

// gateSegment advances the turn's voice gate over one think-filtered segment
// and returns the speakable remainder plus the lines it suppressed. Call it for
// every segment, including ones that will not be spoken.
func (p *SpeechProjection) gateSegment(text string) (string, int) {
	return p.gate.filter(text)
}

type speechAdmissionStatus uint8

const (
	speechNotSpeakable speechAdmissionStatus = iota
	speechAdmitted
	speechAlreadyAdmitted
)

// admit reduces a gated segment to the text handed to the synthesizer,
// distinguishing suppressed content from text already queued in this turn.
func (p *SpeechProjection) admit(voiceText string) (string, speechAdmissionStatus) {
	speakable := brain.CleanForVoice(voiceText)
	// Not just != "": stripping a filler from "Umm." leaves a bare ".", which
	// is non-empty, unpronounceable, and would be glued onto the front of a
	// real sentence inside one synthesis call. A tag-only beat ([moan]) has no
	// speakable prose and must still reach the synth queue so the sound plays.
	if !speakableSegment(speakable) && !hasVocalTag(speakable) {
		return "", speechNotSpeakable
	}
	// A list marker is a whole segment to the chunker and two characters to the
	// listener. Dropping it here and not in the transcript is deliberate: the
	// numbering is worth reading and not worth a synthesis call. Only after the
	// turn has spoken, because "4." on its own is an answer to a question.
	if p.admitted && !hasVocalTag(speakable) && enumeratorOnlySegment(speakable) {
		return "", speechNotSpeakable
	}
	// Sound-only beats ([moan]) may repeat in one turn on purpose. Restated
	// prose after a phantom tool still drops.
	if speakableSegment(stripVocalTags(speakable)) {
		if _, dup := p.spoken[speakable]; dup {
			return "", speechAlreadyAdmitted
		}
		p.spoken[speakable] = struct{}{}
	}
	p.admitted = true
	return speakable, speechAdmitted
}
