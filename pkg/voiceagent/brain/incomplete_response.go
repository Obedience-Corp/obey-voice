package brain

import (
	"context"
	"errors"
	"strings"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain/ollama"
)

// ErrResponseRepetition identifies a model stopped for runaway repetition.
var ErrResponseRepetition = ollama.ErrResponseRepetition

// ForcedAnswer is why a worker was made to answer without tools before its
// work was done. Its text is a fixed phrase the host shows and saves.
type ForcedAnswer string

func (f ForcedAnswer) Error() string { return string(f) }

const (
	ForcedByRepeat   ForcedAnswer = "repeated the same tool call"
	ForcedByTime     ForcedAnswer = "time limit nearly reached"
	ForcedByRequests ForcedAnswer = "request limit reached"
	// ForcedByFailedCorrection: a correction's run failed, so the answer
	// written before it was delivered.
	ForcedByFailedCorrection ForcedAnswer = "the corrected run failed"
	// ForcedByReport: the user asked the job to stop using tools and answer.
	ForcedByReport ForcedAnswer = "report requested"
)

var forcedDispositions = map[ForcedAnswer]string{
	ForcedByRepeat:           "repeated_tool_call",
	ForcedByTime:             "time_limit",
	ForcedByRequests:         "request_limit",
	ForcedByFailedCorrection: "correction_failed",
	ForcedByReport:           "report_requested",
}

// ForcedAnswerReason is the phrase for a forced answer, or "" for any other error.
func ForcedAnswerReason(err error) string {
	var forced ForcedAnswer
	if errors.As(err, &forced) {
		return string(forced)
	}
	return ""
}

// IncompleteDisposition names why an assistant response ended unfinished.
func IncompleteDisposition(err error) string {
	var forced ForcedAnswer
	if errors.As(err, &forced) && forcedDispositions[forced] != "" {
		return forcedDispositions[forced]
	}
	if errors.Is(err, ErrResponseRepetition) {
		return "repetition"
	}
	if errors.Is(err, ErrResponseLimit) {
		return "output_limit"
	}
	if errors.Is(err, context.Canceled) {
		return "interrupted"
	}
	return "failed"
}

// Keep the complete authentic text on the transcript. An unfinished response is
// evidence of a partial attempt, not a completed answer or a fresh instruction.
// Its bounded prefix prevents a canceled generation dominating future turns.
func modelTurnContent(t Turn) string {
	if t.Role != "assistant" && t.Role != "samantha" {
		return t.Content
	}
	if t.HostNote != "" {
		note := t.HostNote
		t.HostNote = ""
		return strings.TrimSpace(modelTurnContent(t) + " " + note)
	}
	if t.Disposition == "" {
		return t.Content
	}
	if t.Disposition == DispositionSuperseded {
		return "[Answer replaced by a later correction from the user; it was not delivered.]\n" + strings.TrimSpace(t.Content)
	}
	// A forced answer is a whole wrap-up, not a runaway tail, so it is kept.
	for forced, disposition := range forcedDispositions {
		if t.Disposition == disposition {
			return "[Incomplete assistant response; not a completed answer. The job was cut short: " + string(forced) + ".]\n" + strings.TrimSpace(t.Content)
		}
	}
	text := []rune(strings.TrimSpace(t.Content))
	const retainedRunes = 1024
	suffix := ""
	if len(text) > retainedRunes {
		text = text[:retainedRunes]
		suffix = " [remaining unfinished output omitted from model context]"
	}
	return "[Incomplete assistant response; not a completed answer.]\n" + string(text) + suffix
}
