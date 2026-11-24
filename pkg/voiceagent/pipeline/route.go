package pipeline

import (
	"regexp"
	"slices"
	"strings"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

// RouteKind is the agency router outcome for one user turn.
type RouteKind string

const (
	RouteNone          RouteKind = ""
	RouteChat          RouteKind = "chat"
	RouteSpawn         RouteKind = "spawn"
	RouteNarrate       RouteKind = "narrate"
	RouteNarratePause  RouteKind = "narrate.pause"
	RouteNarrateResume RouteKind = "narrate.resume"
	RouteNarrateSkip   RouteKind = "narrate.skip"
	RouteNarrateStop   RouteKind = "narrate.stop"
	RouteJobAbort      RouteKind = "job.abort"
	RouteJobStatus     RouteKind = "job.status"
	// RouteJobControl is a live turn that ended at the persona's job_control
	// call; the call itself travels in the turn's metrics.
	RouteJobControl RouteKind = "job.control"
)

// RouteContext supplies mode guards for Layer 1 phrase routing.
type RouteContext struct {
	NarrationActive   bool
	JobActive         bool
	ManagedWorkActive bool // An Obey-managed task is live; it arms the cancel phrases only.
	TieredWorkers     bool
	DirectTools       bool
	LegacyImageWorker bool // A non-tiered worker is available to take background tasks.
}

// RouteDecision records how a turn was classified.
type RouteDecision struct {
	Kind       RouteKind
	Tier       config.WorkerTierName // required for tiered spawn admission
	ReasonCode string
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

	// job.abort — "cancel" and "kill" are the job's verbs; "stop" on its own
	// is speech's, so it is not here.
	{phrase: "stop doing that", kind: RouteJobAbort, requireJob: true},
	{phrase: "cancel that", kind: RouteJobAbort, requireJob: true},
	{phrase: "cancel it", kind: RouteJobAbort, requireJob: true},
	{phrase: "cancel", kind: RouteJobAbort, requireJob: true},
	{phrase: "kill it", kind: RouteJobAbort, requireJob: true},
	{phrase: "kill that", kind: RouteJobAbort, requireJob: true},
	{phrase: "never mind", kind: RouteJobAbort, requireJob: true},
	{phrase: "abort", kind: RouteJobAbort, requireJob: true},

	// job.status
	{phrase: "are you done", kind: RouteJobStatus, requireJob: true},
	{phrase: "what's happening", kind: RouteJobStatus, requireJob: true},
	{phrase: "whats happening", kind: RouteJobStatus, requireJob: true},
	{phrase: "status", kind: RouteJobStatus, requireJob: true},
	{phrase: "any update", kind: RouteJobStatus, requireJob: true},
	{phrase: "is it done yet", kind: RouteJobStatus, requireJob: true},
	{phrase: "hurry up", kind: RouteJobStatus, requireJob: true},
	{phrase: "try again", kind: RouteJobStatus, requireJob: true},
	{phrase: "retry", kind: RouteJobStatus, requireJob: true},
	{phrase: "do it now", kind: RouteJobStatus, requireJob: true},
}

// classifyTurn handles conversation controls and background-work shortcuts before
// the model receives the turn. Direct turns handle ordinary tool requests inline.
func classifyTurn(transcript string, ctx RouteContext) (RouteKind, string, bool) {
	if kind, ok := classifyPhrase(transcript, ctx); ok {
		return kind, "phrase", true
	}
	if ctx.TieredWorkers {
		return RouteNone, "", false
	}
	if ctx.LegacyImageWorker && pictureDeliveryRequest.MatchString(normalizeUtterance(transcript)) {
		return RouteSpawn, "intent", true
	}
	if classifyWorkIntent(transcript, ctx) {
		return RouteSpawn, "intent", true
	}
	return RouteNone, "", false
}

// Match an explicit request for a new picture. A question about a picture
// already in the conversation still belongs to the ordinary router.
var pictureDeliveryRequest = regexp.MustCompile(`^(?:(?:can|could|would|will) you )?(?:please )?(?:show|send) me (?:a picture|a photo|an image) of .+`)

type imageDeliveryKey struct{}

var imageNoun = regexp.MustCompile(`\b(images?|photos?|pictures?|pngs?|jpe?gs?|gifs?)\b`)
var showSubject = regexp.MustCompile(`^(?:yeah,? so )?(?:(?:can|could|would) you )?(?:please )?show me (?:a|an) ([a-z]+)$`)
var addToChat = regexp.MustCompile(`^(?:(?:please|just|you need to|can you|could you) )*(?:add|attach|send|put) (?:it|them|that|those|the (?:images?|photos?|pictures?)) (?:here|(?:in|to) (?:the|this) chat)\b`)

// A job inherits an undelivered picture request only while that request is
// still the subject: one user turn about something else may sit between.
const inheritedImageIdle = 1

func requestsChatImage(task string, history []brain.Turn) bool {
	s := normalizeUtterance(task)
	if pictureDeliveryRequest.MatchString(s) {
		return true
	}
	if match := showSubject.FindStringSubmatch(s); match != nil {
		for _, turn := range slices.Backward(spawnableHistory(history)) {
			if turn.Role == "user" && pictureDeliveryRequest.MatchString(normalizeUtterance(turn.Content)) {
				previous := normalizeUtterance(turn.Content)
				return strings.HasSuffix(previous, " of a "+match[1]) || strings.HasSuffix(previous, " of an "+match[1])
			}
		}
		return false
	}
	// A lookup can end with "and add it to the chat". That clause says where
	// the result goes, not what it is.
	whole := addToChat.MatchString(s)
	tail := false
	if i := strings.LastIndex(s, " and "); i >= 0 {
		tail = addToChat.MatchString(s[i+len(" and "):])
	}
	if !whole && !tail {
		return false
	}
	if whole && imageNoun.MatchString(s) {
		return true
	}
	// "Find a picture of a dog and add it to the chat" asks for the picture
	// itself. "Write a caption for my photo and add it" only mentions one.
	if !whole && brain.AsksForPicture(task) {
		return true
	}
	before := history
	if n := len(before); n > 0 && before[n-1].Role == "user" && before[n-1].Content == task {
		before = before[:n-1]
	}
	if owed, ok := brain.FindOwedImage(before); ok && owed.Idle <= inheritedImageIdle && owed.AskedAfter(task) {
		return true
	}
	if !whole {
		return false
	}
	// Resolve "add them to the chat" against the most recent substantive
	// exchange. Do not revive an image task from before a change of subject.
	for _, turn := range slices.Backward(spawnableHistory(history)) {
		if turn.Content == task {
			continue
		}
		if len(turn.Attachments) > 0 || imageNoun.MatchString(strings.ToLower(turn.Content)) {
			return true
		}
		if turn.Role == "user" {
			break
		}
	}
	return false
}

// Only consult conversation history for a follow-up that refers to a deliverable.
func (p *Pipeline) classifyUserTurn(input string) (RouteKind, string, bool) {
	ctx := p.routeContext()
	if kind, source, ok := classifyTurn(input, ctx); ok {
		return kind, source, true
	}
	s := normalizeUtterance(input)
	if ctx.LegacyImageWorker && (showSubject.MatchString(s) || addToChat.MatchString(s)) && requestsChatImage(input, p.Agency.liveHistory()) {
		return RouteSpawn, "intent", true
	}
	return RouteNone, "", false
}

// These longer requests retain background execution when a worker is available.
var backgroundIntents = []string{
	"search the web",
	"search web",
	"build this",
	"implement this",
	"implement a",
}

// Routed mode also sends ordinary tool operations to a worker.
var spawnIntents = []string{
	"search for",
	"look up",
	"look this up",
	"google",
	"create a file",
	"write a file",
	"write this to",
	"run this command",
	"run a command",
	"filter design",
	"filter the work",
	"filter work items",
}

func classifyWorkIntent(transcript string, ctx RouteContext) bool {
	s := normalizeUtterance(transcript)
	if ctx.DirectTools {
		// Keep long-running work off the mic when a worker can take it.
		return ctx.LegacyImageWorker && hasIntentPrefix(s, backgroundIntents)
	}
	return hasIntentPrefix(s, backgroundIntents) || hasIntentPrefix(s, spawnIntents)
}

func hasIntentPrefix(s string, prefixes []string) bool {
	for _, prefix := range prefixes {
		if s == prefix || strings.HasPrefix(s, prefix+" ") || strings.HasPrefix(s, prefix+",") {
			return true
		}
	}
	return false
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
		if entry.requireJob && !ctx.JobActive && !(entry.kind == RouteJobAbort && ctx.ManagedWorkActive) {
			return RouteNone, false
		}
		return entry.kind, true
	}
	return RouteNone, false
}

// normalizeUtterance folds case and whitespace and drops the punctuation a
// transcriber or a typist puts at the ends: "Kill it." is "kill it".
func normalizeUtterance(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	s = strings.ToLower(s)
	s = strings.Join(strings.Fields(s), " ")
	return strings.Trim(s, ".!?,;:")
}

// routeKindSkipsBrain reports whether a Layer 1 match should not call ThinkStream.
func routeKindSkipsBrain(kind RouteKind) bool {
	switch kind {
	case RouteSpawn, RouteNarrate,
		RouteNarratePause, RouteNarrateResume, RouteNarrateSkip, RouteNarrateStop,
		RouteJobAbort, RouteJobStatus, RouteJobControl:
		return true
	default:
		return false
	}
}

// Promise repair requires an explicit first-person commitment to tool work.
// Merely discussing tools, commands, or execution is ordinary conversation.
var toolPromiseRE = regexp.MustCompile(`(?i)\bi(?: will| shall|'ll|’ll| am|'m|’m) (?:going to |now )?(?:(?:check|filter|inspect|review|search|gather)\b[^.!?;\n]{0,80} and )?(?:use|using|invoke|invoking|run|running|execute|executing|call|calling|compose|composing|prepare|preparing)\b[^.!?;\n]{0,160}\b(?:tool|tools|command|commands)\b`)

func isToolPromise(text string) bool {
	return toolPromiseRE.MatchString(text)
}

// spokenToolPromise reads cleaned reply text as failed-route repair does,
// without stage directions or line breaks, so every caller agrees on a promise.
func spokenToolPromise(cleaned string) bool {
	return isToolPromise(strings.Join(strings.Fields(stripVocalTags(cleaned)), " "))
}

// promiseRepairState tracks Layer 3 failed-route repair during streamResponse.
// Repair runs after ThinkStream: if the model spoke a tool promise with no tool
// events, drop TTS and classify spawn instead of ending the turn as chat.
type promiseRepairState struct {
	enabled    bool
	toolsFired bool
	spawn      bool
	kind       RouteKind
	source     string
}

// failedRouteRepairEnabled is intentionally false for tiered workers. Their
// live route tool is required to create an explicitly tiered spawn; guessing a
// spawn from spoken promise text would lose that required admission data.
func (p *Pipeline) failedRouteRepairEnabled() bool {
	tiered := p.Agency != nil && tieredWorkers(p.Agency.currentConfig())
	return p.VoiceFailedRouteRepair && !tiered
}

func (r *promiseRepairState) shouldRepair(accumulated string) bool {
	if r == nil || !r.enabled || r.toolsFired || r.spawn || routeKindSkipsBrain(r.kind) {
		return false
	}
	if spokenToolPromise(accumulated) {
		r.spawn = true
		r.kind = RouteSpawn
		r.source = "repair"
		return true
	}
	return false
}

func (r *promiseRepairState) applyBrainRoute(route string) {
	if r == nil {
		return
	}
	kind := routeKindFromBrain(route)
	if !routeKindSkipsBrain(kind) {
		return
	}
	r.kind = kind
	r.source = "router"
}

func (r *promiseRepairState) routedKind() (RouteKind, string, bool) {
	if r == nil {
		return RouteNone, "", false
	}
	if routeKindSkipsBrain(r.kind) {
		source := r.source
		if source == "" {
			source = "router"
		}
		return r.kind, source, true
	}
	if r.spawn {
		return RouteSpawn, "repair", true
	}
	return RouteNone, "", false
}

func routeKindFromBrain(route string) RouteKind {
	switch strings.ToLower(strings.TrimSpace(route)) {
	case "spawn":
		return RouteSpawn
	case "narrate":
		return RouteNarrate
	case brain.JobControlRoute:
		return RouteJobControl
	default:
		return RouteChat
	}
}
