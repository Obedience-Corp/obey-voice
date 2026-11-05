package brain

import (
	"context"
	"errors"
	"strings"
)

func incompleteDisposition(err error) string {
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
	if t.Disposition == "" || (t.Role != "assistant" && t.Role != "samantha") {
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
