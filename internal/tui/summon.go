package tui

import (
	"cmp"
	"strings"
	"sync/atomic"

	"github.com/Obedience-Corp/obey-voice/internal/summon"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

// nameSummonOffMessage is what a session says when the config asks for name
// summon but the machine cannot host it safely (D5 fails closed).
const nameSummonOffMessage = "Name summon is on but the voice front end is off — leaving it off this session so playback cannot summon the agent. See obey-voice doctor."

// summonGate is this session's name summon: the wake words a finalized
// transcript must open with, and the flag that stands the whole thing down
// while the talk key owns the turn.
type summonGate struct {
	matcher summon.Matcher
	agent   string
	// keyTurn is shared with the pipeline goroutine that evaluates the gate,
	// which is why it is atomic and not a plain bool on the model.
	keyTurn *atomic.Bool
}

// veto is the pipeline's AddressGate. An empty string lets the turn run.
func (g *summonGate) veto(text string) string {
	if g.keyTurn.Load() || g.matcher.Addressed(text) {
		return ""
	}
	return "Not addressed to " + g.agent + " — ignored: “" + excerpt(text, 48) + "”"
}

// names renders the wake words for the activity panel.
func (g *summonGate) names() string {
	return strings.Join(g.matcher.Names(), ", ")
}

// addressGate builds this session's name summon and returns the pipeline hook.
// Nil when the persona has no name to answer to: a matcher with no wake words
// matches nothing, and installing it would silently swallow every utterance.
func (m *conversationModel) addressGate(cfg *config.Config) func(string) string {
	matcher := summon.NewMatcher(m.agentName, cfg.NameSummonAliases)
	if len(matcher.Names()) == 0 {
		return nil
	}
	if m.talkKeyTurn == nil {
		m.talkKeyTurn = new(atomic.Bool)
	}
	m.nameSummon = &summonGate{matcher: matcher, agent: m.agentName, keyTurn: m.talkKeyTurn}
	return m.nameSummon.veto
}

// keyOwnsTurn records whether the talk key opened the turn now in flight.
// Pressing the key is itself an address, so name summon stands down for it.
func (m *conversationModel) keyOwnsTurn(owns bool) {
	if m.talkKeyTurn != nil {
		m.talkKeyTurn.Store(owns)
	}
}

// installNameSummon wires the name gate onto the session's pipeline, or says
// why it is not wired. Called after startConversation: the event bus and the
// activity panel it writes to are set up there, and the first turn is still
// only a queued Cmd at that point.
func (a *App) installNameSummon(rt *ConversationRuntime) {
	cfg := cmp.Or(rt.SessionCfg, a.cfg)
	if cfg == nil || !cfg.NameSummonEnabled || rt.Pipeline == nil {
		return
	}
	// Without a front-end the microphone hears the speakers, so the agent
	// would answer its own voice saying its own name (D5, R2).
	if config.NameSummonSelfTriggerRisk(cfg) {
		a.conversation.emit(events.Info{Message: nameSummonOffMessage})
		return
	}
	// Persistent sessions route finalized input through the shared selector.
	// Its admission path owns addressing and response identity, so an old
	// per-turn gate would reject nameless follow-ups after a selection.
	if rt.Pipeline.AdmitInput != nil {
		return
	}
	gate := a.conversation.addressGate(cfg)
	if gate == nil {
		return
	}
	rt.Pipeline.AddressGate = gate
	a.conversation.appendActivity("name summon", a.conversation.nameSummon.names(), 0)
}

// excerpt shortens text for a one-line message, counting runes so a multi-byte
// transcript is not cut mid-character.
func excerpt(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit-1]) + "…"
}
