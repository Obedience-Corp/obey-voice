package delegation

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"
)

const MaxBriefBytes = 1200

// MaxTaskBytes reserves space for our worker instruction within Obey's context cap.
const MaxTaskBytes = 2048 - len(workerInstruction)
const workerInstruction = "\n\nReturn the result in two or three short spoken sentences. No markdown, tables, code, or JSON. State limitations clearly."

// Summarizer must be stateless: it must not mutate a live conductor history.
type Summarizer func(context.Context, string) (string, error)

func speakable(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" || len(s) > MaxBriefBytes || !utf8.ValidString(s) || json.Valid([]byte(s)) {
		return false
	}
	if strings.ContainsAny(s, "{}[]`#|*_<>\\") {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return false
		}
	}
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "- ") || strings.HasPrefix(line, "+ ") || (len(line) > 2 && line[0] >= '0' && line[0] <= '9' && line[1] == '.') {
			return false
		}
	}
	return true
}

func makeBrief(ctx context.Context, s Snapshot, summarize Summarizer) (string, error) {
	switch s.State {
	case "canceled":
		return "The background task was canceled.", nil
	case "failed", "rejected", "interrupted", "cleanup_failed":
		return "The background task did not finish successfully. Its details are available in Obey.", nil
	case "completed":
	default:
		return "", errors.New("cannot brief a nonterminal delegation")
	}
	if !s.Truncated && speakable(s.Text) {
		return strings.Join(strings.Fields(s.Text), " "), nil
	}
	if summarize != nil && s.Text != "" {
		text := s.Text
		if s.Truncated {
			text = "The worker result was truncated; mention that the summary covers only part of the result.\n\n" + text
		}
		if len(text) > 64*1024 {
			text = text[:64*1024]
			for !utf8.ValidString(text) {
				text = text[:len(text)-1]
			}
		}
		brief, err := summarize(ctx, text)
		if err == nil && speakable(brief) {
			return strings.Join(strings.Fields(brief), " "), nil
		}
	}
	// Raw artifacts are never used as a fallback, including failed summaries.
	return "The background task finished. I could not prepare a short spoken summary; its result is available in Obey.", nil
}
