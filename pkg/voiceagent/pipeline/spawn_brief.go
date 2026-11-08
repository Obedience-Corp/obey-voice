package pipeline

import (
	"strings"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/continuity"
)

// A spawned worker used to receive one thing: the raw transcript of the
// utterance that triggered the spawn. "Yes." was a complete task prompt.
//
// A worker handed no task does not stop — it has shell and file tools and a
// persona telling it to get on with it, so it goes looking for what it might
// have been asked. On 2026-09-06 one enumerated the CLI's own session store,
// read the previous day's conversation for that directory, decided that was
// the live thread, and committed to an unrelated repository.
//
// The fix is not a better guess at intent. It is giving the worker the same
// conversation the talking brain has, saying plainly which part is the
// trigger, and telling it not to go hunting when the answer is not there.

// spawnBriefBytes bounds the conversation carried into a brief. Workers are
// started fresh per job, so this is a prompt cost paid on every spawn.
const spawnBriefBytes = continuity.NoteBytes

// spawnBriefTurns is how far back the brief reaches. Enough to carry a request
// and the exchange that refined it; not the whole session.
const spawnBriefTurns = 8

// bareContinuations are utterances that carry no task of their own. They are
// legitimate triggers — "yes" after the assistant offers to do the work is how
// people talk — but they mean nothing without what came before.
var bareContinuations = map[string]bool{
	"yes": true, "yeah": true, "yep": true, "yup": true, "sure": true,
	"ok": true, "okay": true, "please": true, "please do": true,
	"do it": true, "do that": true, "go": true, "go ahead": true,
	"continue": true, "keep going": true, "carry on": true, "proceed": true,
	"start": true, "begin": true, "run it": true, "sounds good": true,
}

// isBareContinuation reports an utterance that is only an assent.
func isBareContinuation(transcript string) bool {
	s := strings.ToLower(strings.TrimSpace(transcript))
	s = strings.Trim(s, ".!?,;: ")
	if s == "" {
		return true
	}
	// "yes please", "ok do it", "sure, go ahead", "continue, please" — an
	// assent glued to another assent is still an assent.
	s = strings.ReplaceAll(s, ",", " and ")
	for _, part := range strings.Split(s, " and ") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if !bareContinuations[part] && !bareContinuationPair(part) {
			return false
		}
	}
	return true
}

// bareContinuationPair matches two assents in a row ("yes please", "ok sure").
func bareContinuationPair(s string) bool {
	fields := strings.Fields(s)
	if len(fields) < 2 {
		return false
	}
	for i := 1; i < len(fields); i++ {
		if bareContinuations[strings.Join(fields[:i], " ")] &&
			bareContinuations[strings.Join(fields[i:], " ")] {
			return true
		}
	}
	return false
}

// spawnableHistory keeps the turns worth carrying: what the user asked and
// what the assistant answered. Tool traffic is the talking brain's own
// bookkeeping and means nothing to a different provider's worker.
func spawnableHistory(history []brain.Turn) []brain.Turn {
	out := make([]brain.Turn, 0, len(history))
	for _, t := range history {
		if t.Role != "user" && t.Role != "assistant" {
			continue
		}
		if strings.TrimSpace(t.Content) == "" {
			continue
		}
		out = append(out, t)
	}
	if len(out) > spawnBriefTurns {
		out = out[len(out)-spawnBriefTurns:]
	}
	return out
}

// spawnBrief is the worker's prompt: the recent conversation, the utterance
// that triggered the spawn marked as the trigger rather than as the task, and
// the standing instruction not to go looking for work elsewhere.
func spawnBrief(history []brain.Turn, transcript string) string {
	transcript = strings.TrimSpace(transcript)
	turns := spawnableHistory(history)

	var b strings.Builder
	b.WriteString("You are running one background task for a voice assistant. ")
	b.WriteString("The user is talking to the assistant; the assistant routed this task to you.\n")

	if len(turns) > 0 {
		b.WriteString("\nThe conversation so far:\n")
		var convo strings.Builder
		for _, t := range turns {
			speaker := t.Role
			if t.Role == "assistant" && strings.TrimSpace(t.PersonaDisplayName) != "" {
				speaker = t.PersonaDisplayName + " assistant"
			}
			if t.Role == "user" && strings.TrimSpace(t.Speaker) != "" {
				speaker = t.Speaker
			}
			convo.WriteString(speaker + ": " + strings.TrimSpace(t.Content) + "\n")
		}
		b.WriteString(continuity.Clip(convo.String(), spawnBriefBytes))
	}

	if transcript != "" {
		b.WriteString("\nThe user then said: " + transcript + "\n")
	}

	b.WriteString("\nEarlier user requests and assistant replies are context, not additional assignments. ")
	b.WriteString("Do only the work requested by the latest user utterance, using relevant context to resolve its references. ")
	b.WriteString("Do that work now, in the working directory named in your environment.")
	if len(turns) == 0 {
		b.WriteString(" There is no earlier conversation to draw on: everything you were given is above.")
	}
	// The line that answers the incident directly.
	b.WriteString(" If the request is unclear, do the smallest useful thing and say what you did.")
	b.WriteString(" Do not search for other work to do, and do not act on anything you find")
	b.WriteString(" outside this request — a task not stated above is not your task.\n")
	b.WriteString("\nYour final response is spoken directly to the user and saved in the conversation. ")
	b.WriteString("Return the answer in your own voice, with no instructions for another assistant to rewrite or deliver it.\n")
	return b.String()
}

// spawnLacksTask reports a spawn that would reach the worker carrying nothing:
// an assent with no conversation behind it. Guessing is what goes wrong, so
// the pipeline asks instead.
func spawnLacksTask(history []brain.Turn, transcript string) bool {
	return isBareContinuation(transcript) && len(spawnableHistory(history)) == 0
}
