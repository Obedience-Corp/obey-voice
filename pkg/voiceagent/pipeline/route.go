package pipeline

import (
	"strings"
)

// RouteKind is the agency router outcome for one user turn.
type RouteKind string

const (
	RouteNone         RouteKind = ""
	RouteChat         RouteKind = "chat"
	RouteSpawn        RouteKind = "spawn"
	RouteNarrate      RouteKind = "narrate"
	RouteNarratePause RouteKind = "narrate.pause"
	RouteNarrateResume RouteKind = "narrate.resume"
	RouteNarrateSkip  RouteKind = "narrate.skip"
	RouteNarrateStop  RouteKind = "narrate.stop"
	RouteJobAbort     RouteKind = "job.abort"
	RouteJobStatus    RouteKind = "job.status"
)

// RouteContext supplies mode guards for Layer 1 phrase routing.
type RouteContext struct {
	NarrationActive bool
	JobActive       bool
}

// RouteDecision records how a turn was classified.
type RouteDecision struct {
	Kind       RouteKind
	Transcript string
	Source     string // "phrase", "repair"
}

type phraseEntry struct {
	phrase           string
	kind             RouteKind
	requireNarration bool
	requireJob       bool
}

// phraseRoutes is ordered most-specific first so overlaps resolve deterministically.
var phraseRoutes = []phraseEntry{
	// narrate.stop — longer phrases before any shared prefix.
	{phrase: "stop reading", kind: RouteNarrateStop, requireNarration: true},
	{phrase: "stop the story", kind: RouteNarrateStop, requireNarration: true},
	{phrase: "that's enough", kind: RouteNarrateStop, requireNarration: true},
	{phrase: "thats enough", kind: RouteNarrateStop, requireNarration: true},

	// narrate
	{phrase: "tell me a long story", kind: RouteNarrate},
	{phrase: "read this", kind: RouteNarrate},
	{phrase: "read the article", kind: RouteNarrate},
	{phrase: "read chapter", kind: RouteNarrate},
	{phrase: "audiobook", kind: RouteNarrate},
	{phrase: "play the book", kind: RouteNarrate},

	// narrate.pause
	{phrase: "pause", kind: RouteNarratePause, requireNarration: true},
	{phrase: "hold on", kind: RouteNarratePause, requireNarration: true},
	{phrase: "wait", kind: RouteNarratePause, requireNarration: true},

	// narrate.resume
	{phrase: "keep going", kind: RouteNarrateResume, requireNarration: true},
	{phrase: "continue", kind: RouteNarrateResume, requireNarration: true},
	{phrase: "resume", kind: RouteNarrateResume, requireNarration: true},

	// narrate.skip
	{phrase: "next section", kind: RouteNarrateSkip, requireNarration: true},
	{phrase: "next chapter", kind: RouteNarrateSkip, requireNarration: true},
	{phrase: "skip", kind: RouteNarrateSkip, requireNarration: true},

	// job.abort
	{phrase: "stop doing that", kind: RouteJobAbort, requireJob: true},
	{phrase: "cancel that", kind: RouteJobAbort, requireJob: true},
	{phrase: "never mind", kind: RouteJobAbort, requireJob: true},
	{phrase: "abort", kind: RouteJobAbort, requireJob: true},

	// job.status
	{phrase: "are you done", kind: RouteJobStatus, requireJob: true},
	{phrase: "what's happening", kind: RouteJobStatus, requireJob: true},
	{phrase: "whats happening", kind: RouteJobStatus, requireJob: true},
	{phrase: "status", kind: RouteJobStatus, requireJob: true},
}

// classifyPhrase matches the whole utterance against Layer 1 phrases.
// ok is false when no phrase matches or a mode guard fails (fall through to chat).
func classifyPhrase(transcript string, ctx RouteContext) (RouteKind, bool) {
	normalized := normalizeUtterance(transcript)
	if normalized == "" {
		return RouteNone, false
	}
	for _, entry := range phraseRoutes {
		if normalized != entry.phrase {
			continue
		}
		if entry.requireNarration && !ctx.NarrationActive {
			return RouteNone, false
		}
		if entry.requireJob && !ctx.JobActive {
			return RouteNone, false
		}
		return entry.kind, true
	}
	return RouteNone, false
}

func normalizeUtterance(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	s = strings.ToLower(s)
	return strings.Join(strings.Fields(s), " ")
}

// routeKindSkipsBrain reports whether a Layer 1 match should not call ThinkStream.
func routeKindSkipsBrain(kind RouteKind) bool {
	switch kind {
	case RouteSpawn, RouteNarrate,
		RouteNarratePause, RouteNarrateResume, RouteNarrateSkip, RouteNarrateStop,
		RouteJobAbort, RouteJobStatus:
		return true
	default:
		return false
	}
}

// isToolPromise detects spoken tool-intent prose with no structured tool_calls (D3).
// Keep tight: tool/command/invoke/execute language, not casual "I shall have a think".
func isToolPromise(text string) bool {
	lower := strings.ToLower(strings.TrimSpace(text))
	if lower == "" {
		return false
	}
	hasIntent := strings.Contains(lower, "tool") ||
		strings.Contains(lower, "command") ||
		strings.Contains(lower, "invoke") ||
		strings.Contains(lower, "execut")
	if !hasIntent {
		return false
	}
	// Negative: casual phrasing without tool-work intent.
	if strings.Contains(lower, "have a think") {
		return false
	}
	return true
}

// promiseRepairState tracks Layer 3 failed-route repair during streamResponse.
// Repair runs after ThinkStream: if the model spoke a tool promise with no tool
// events, drop TTS and classify spawn instead of ending the turn as chat.
type promiseRepairState struct {
	enabled    bool
	toolsFired bool
	spawn      bool
}

func (r *promiseRepairState) shouldRepair(accumulated string) bool {
	if r == nil || !r.enabled || r.toolsFired || r.spawn {
		return false
	}
	cleaned := strings.TrimSpace(accumulated)
	if cleaned == "" {
		return false
	}
	if isToolPromise(cleaned) {
		r.spawn = true
		return true
	}
	return false
}
