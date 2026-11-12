package brain

import (
	"context"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/chatmedia"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/utterance"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/continuity"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/discussion"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

// StreamOptions controls how a streamed brain response should behave.
type StreamOptions struct {
	// RetryInput resumes a validated failed input without appending another user turn.
	RetryInput bool
	// InputBindingRevision fences explicit retries across host binding changes.
	InputBindingRevision uint64
	// HarnessRouting describes the host's conversational path after classification
	// or routing-context recovery. It is never a recorded user instruction.
	HarnessRouting *HarnessRouting
	Utterance      *utterance.Finalized
	AudioRetained  bool
	Attachments    []chatmedia.Ref
	Discussion     *discussion.Directive
	// WorkerBudget is the job-shared operational budget for this worker job.
	WorkerBudget  *WorkerBudget
	OnWorkerUsage func(WorkerUsage)
	// WorkerTiers is the live, policy-only availability snapshot for the route
	// tool. It contains no provider, model, or pricing information.
	WorkerTiers *WorkerTierAvailability
	// WorkerFacts are bounded identifiers for existing jobs, supplied for
	// grounded status references. They contain no task text or provider data.
	WorkerFacts []WorkerFact
	// OriginalInput preserves finalized user text when the host routes a
	// stripped request. It changes canonical storage, not the model request.
	OriginalInput string
	// TransientHistory keeps an internal summary request and response out of
	// canonical history. Projection-owning providers restore both histories
	// after this request; the host then installs the successful summary view.
	TransientHistory bool
	// Identity is the immutable identity of an admitted assistant response.
	// Providers that support attributed history copy it onto the resulting
	// assistant turn; nil preserves legacy unattributed behavior.
	Identity *TurnIdentity
	// WorkResults is request-private canonical result context. Providers may
	// include it in the next prompt, but must never append it to history.
	WorkResults []CompletionResult
	// CatchUp is the host transcript a provider keeping its own session may
	// receive. Providers render it as request-local conversation record and
	// never append it to history.
	CatchUp *SessionCatchUp
	// Background marks a host-scheduled result notification, not a user utterance.
	Background bool
	// AgentContinuation means the latest agent in shared canonical history is
	// handing the floor to this persona. The provider must not append input as
	// a fabricated user turn; it records only the resulting assistant reply.
	AgentContinuation bool
	VoiceMode         bool
	ToolsEnabled      bool
	// Speaker is the stable live speaker id for this user turn (speaker-1),
	// when live labels are active. Empty means ordinary single-user "User".
	// Stored on history turns and used for prompt attribution.
	Speaker string
	// OnPromptWarn reports a non-fatal prompt-resolution failure for this turn
	// — typically a half-saved persona document. The turn continues on the last
	// good prompt, so this is a warning the UI should surface rather than an
	// error that ends the turn: bricking a live conversation because a file was
	// mid-save is worse than speaking with a slightly stale personality.
	OnPromptWarn func(message string)
	// OnToolStart is invoked just before a tool runs (optional; for UI).
	OnToolStart func(ToolCall)
	// OnToolEnd is invoked after a tool returns (optional; for UI).
	OnToolEnd func(ToolResult)
	// OnUsage reports one model request's token accounting (optional; for
	// UI). prefill is the prompt tokens the server actually processed — with
	// a warm KV prefix cache it should track the size of the new turn, not
	// the whole transcript — and gen is the tokens generated.
	OnUsage func(prefill, gen int)
	// OnSessionWarn reports that the provider's replayed session prompt has
	// crossed the configured warn threshold (optional; for UI). Fired at most
	// once per underlying session; the session itself is left alone.
	OnSessionWarn func(promptTokens, threshold int)
	// OmitTurnInstruction drops the per-turn instruction from the prompt.
	// Set it for meta turns whose output is not a spoken reply — /compact's
	// summarize turn — where the turn instruction's voice framing ("2–3
	// sentences max") lands after the real instruction and starves it.
	OmitTurnInstruction bool
	// RouterToolsPolicy selects live route_only vs full worker tools when
	// ToolsEnabled is true. Empty keeps the full worker list; the microphone
	// path passes route_only explicitly.
	RouterToolsPolicy RouterToolsPolicy
	// RouteJSON has a route_only Ollama turn decide its route with a tools-free
	// JSON request on the reply's own messages (voice_router_tools: json_route).
	// Other providers ignore it.
	RouteJSON bool
}

func (o StreamOptions) workerBudget() *WorkerBudget {
	return o.WorkerBudget
}

type WorkerUsage struct {
	InputTokens    *int
	OutputTokens   *int
	ChargeMicroUSD *int64
}

// PersonaPromptFreezer pins a newly built responder's resolved identity text.
// Call before admitting responses; the returned hash describes that text.
// Shared-persona hosts resolve edits at the next response boundary themselves.
type PersonaPromptFreezer interface {
	FreezePersonaPrompt() string
}

// ToolCall describes one tool invocation for the UI hooks.
type ToolCall struct {
	// ID is the model's own call id when it sent one, else "<name>#<n>"
	// synthesized per session so consumers can pair start with end.
	ID      string
	Name    string
	Kind    ToolKind
	Summary string
}

// ToolResult describes a finished tool invocation for the UI hooks.
type ToolResult struct {
	ToolCall
	Preview string
	// Err is legacy error detail when available; absence does not establish
	// success. Use Outcome for observed status.
	Err     string
	Elapsed time.Duration
	// Outcome contains observed execution facts when the provider can establish
	// them. Opaque providers and synthetic route/skill hooks leave it unknown.
	Outcome *events.ToolOutcome
}

// IntegrityResult reports whether a streamed completion may have been
// truncated before the model finished. Populated on the Ollama path when
// continuity_integrity_enabled is true (WI-b94dad S1).
type IntegrityResult struct {
	Truncated    bool
	MidSentence  bool
	SpokenSoFar  string
	PromptTokens int
	GenTokens    int
	DoneReason   string
}

// StreamResult reports the terminal outcome of a streamed response.
type StreamResult struct {
	Err error
	// Recovered means any emitted partial text can drain normally despite Err.
	// Consumers complete degraded and display the error separately. Providers
	// must not inject synthetic error speech into Chunks.
	Recovered bool
	// Integrity is set when continuity_integrity_enabled and the provider
	// detected a probable truncation. S5 owns audible recovery; S1 emits
	// events only.
	Integrity IntegrityResult
	// Route is set when the live router classified this turn as work instead
	// of chat. Values are "spawn" or "narrate"; empty means the turn is chat.
	Route string
	// RouteTier and RouteReasonCode carry the route tool's bounded metadata.
	RouteTier       config.WorkerTierName
	RouteReasonCode string
}

// WorkerTierAvailability is the only worker policy exposed to the live
// classifier. A nil pointer preserves the legacy route schema.
type WorkerTierAvailability struct {
	Simple  bool
	Medium  bool
	Complex bool
}

type WorkerFact struct {
	ID    string
	Tier  config.WorkerTierName
	State string
}

// Stream carries a streamed model response and its terminal result.
type Stream struct {
	// Chunks are speakable content only. No think tags, no Thinking-field
	// bytes.
	Chunks <-chan string
	// Thinking is operator-facing reasoning. Pipeline must not segment,
	// gate, or synthesize it. TUI paints it dim.
	//
	// Contract: nil when the provider has nothing to report (Claude, Grok,
	// stubs, scriptedStream). Not a closed channel. A closed channel in a
	// Go select delivers the zero value forever; nil disables the case.
	// Ollama allocates and closes both channels when the goroutine exits.
	Thinking <-chan string
	Done     <-chan StreamResult
}

// Provider is the interface for all brain backends (Claude, Ollama, etc.).
// FreshProvider is a Provider that can hand out a sibling with no history
// and no CLI session, sharing its client, prompts and catalog. A worker job
// is one conversation; the job after it must not inherit that one, and two
// jobs running at once must not write into the same one.
type FreshProvider interface {
	Provider
	Fresh() Provider
}

type Provider interface {
	ThinkStream(ctx context.Context, input string, opts StreamOptions) (*Stream, error)
	// ThinkFull runs a non-streaming turn. opts.ToolsEnabled is the sole
	// runtime gate for tool calls — callers (pipeline) pass the same flag
	// used for ThinkStream so text and voice paths cannot diverge.
	//
	// On error, history must be left exactly as it was: implementations that
	// append the input before the model call (the prompt is built from
	// history) have to roll that back. Callers cannot clean up for them —
	// LoadHistory would drop a live harness session — and /compact relies on
	// it, or a failed summarize turn leaves its briefing in the conversation.
	ThinkFull(ctx context.Context, input string, opts StreamOptions) (string, error)
	ClearHistory()
	History() []Turn
	LoadHistory(turns []Turn)
}

// HistoryProjectionProvider restores canonical turns into a live provider
// without invoking the explicit session-resetting LoadHistory lifecycle.
type HistoryProjectionProvider interface{ ImportHistory([]Turn) error }

// CanonicalHistoryProvider exposes the unbounded transcript authority when a
// caller needs to persist it separately from a provider's native projection.
type CanonicalHistoryProvider interface{ CanonicalHistory() []Turn }

// KeepsSharedHistory reports whether a provider records the shared, attributed
// transcript itself and accepts it back on a persona switch. The host records
// the transcript for every other provider.
func KeepsSharedHistory(p Provider) bool {
	switch p.(type) {
	case HistoryProjectionProvider, ContinuityTransferProvider:
		return true
	}
	return false
}

// KeepsPersonaSession reports whether a provider keeps a session of its own
// for its persona across persona switches. The host parks such a provider
// while another persona speaks instead of retiring it, and brings its session
// up to date with StreamOptions.CatchUp instead of projecting history into it.
func KeepsPersonaSession(p Provider) bool {
	_, ok := p.(interface{ keepsPersonaSession() })
	return ok
}

func (*Brain) keepsPersonaSession()       {}
func (*GrokBrain) keepsPersonaSession()   {}
func (*CursorBrain) keepsPersonaSession() {}

// ParksSession reports whether the host parks a provider while another
// persona speaks, instead of retiring it, and takes it up again when that
// persona is addressed: a provider keeping its own session, or one keeping
// the shared history in a native session that resumes with only the lines it
// has not been sent.
func ParksSession(p Provider) bool {
	_, ok := p.(interface{ parksSession() })
	return ok || KeepsPersonaSession(p)
}

// ContextHistoryProvider exposes the bounded model view for compaction only.
// Its projected text is never an authority for persisted speaker identity.
type ContextHistoryProvider interface{ ContextHistory() []Turn }

// ContinuitySnapshot is the provider's durable transcript together with the
// bounded view currently retained for model requests. The working view may
// contain a compact seed and must not be expanded back to Canonical.
type ContinuitySnapshot struct {
	Canonical        []Turn
	Working          []Turn
	Archive          []continuity.Entry
	ContextHighWater int
	// Speakers names the personas with replies in a hosted persona's share,
	// which its working view may since have summarized or trimmed away.
	Speakers []string
}

// ContinuityTransferProvider moves an active conversation between persona
// providers without discarding compaction. Implementations must reproject the
// working view for their own persona.
type ContinuityTransferProvider interface {
	ContinuitySnapshot() ContinuitySnapshot
	ImportContinuity(ContinuitySnapshot) error
}

// IdentityHistoryAppender admits host-authored responses with immutable
// historical identity. Providers without it remain legacy-compatible.
type IdentityHistoryAppender interface {
	AppendAssistantResponseWithIdentity(string, TurnIdentity) error
}

// Warmer is an optional Provider capability that preloads the backend (e.g.
// loads the model into memory) so the user's first turn avoids the cold-start
// cost. Implementations are best-effort and must not block on failures.
type Warmer interface {
	Warmup(ctx context.Context)
}

// SessionKind values for SessionState.Kind, shared by implementers and
// consumers so a typo cannot silently fall into the wrong display branch.
const (
	SessionKindHarness = "harness"
	SessionKindChat    = "chat"
)

// SessionState is a point-in-time description of who owns the conversation
// and how big it has grown, for display (the /session command).
type SessionState struct {
	// Kind is SessionKindHarness (a CLI session owns the transcript) or
	// SessionKindChat (samantha owns messages in-process).
	Kind string
	// ID is the harness session id being resumed; empty when none is live.
	ID string
	// PromptTokens is the last known replayed-prompt size (harness) or the
	// estimated next-request size (chat). 0 when unknown.
	PromptTokens int
	// Turns is the local history length retained for persistence/UI.
	Turns int
}

// SessionReporter is an optional Provider capability exposing live session
// state for UI display.
type SessionReporter interface {
	SessionInfo() SessionState
}
