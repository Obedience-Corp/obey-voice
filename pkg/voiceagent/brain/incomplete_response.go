package brain

import (
	"context"
	"errors"
	"strings"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain/ollama"
)

// ErrResponseRepetition identifies a model stopped for runaway repetition.
var ErrResponseRepetition = ollama.ErrResponseRepetition

// IncompleteDisposition names why an assistant response ended unfinished.
func IncompleteDisposition(err error) string {
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
	text := []rune(strings.TrimSpace(t.Content))
	const retainedRunes = 1024
	suffix := ""
	if len(text) > retainedRunes {
		text = text[:retainedRunes]
		suffix = " [remaining unfinished output omitted from model context]"
	}
	return "[Incomplete assistant response; not a completed answer.]\n" + string(text) + suffix
}
