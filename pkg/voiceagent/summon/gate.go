package summon

import (
	"errors"
	"sync"
	"sync/atomic"
	"time"
)

// ErrNoName is returned when a gate is asked for with nothing to answer to.
// A gate with no names would drop every utterance it was shown.
var ErrNoName = errors.New("summon: the agent has no name or alias to answer to")

// Gate is name summon for one agent: it decides whether a finalized transcript
// may open a turn. Veto has the shape of pipeline.Pipeline.AddressGate, so a
// host installs it with one assignment, and voiceagent.New does so itself when
// name_summon_enabled is set.
//
// A turn is let through when the utterance opens with one of the agent's
// names, when the host has declared the turn addressed some other way
// (SetBypass), or, if a follow-up window was asked for, when the agent has
// just finished replying.
//
// A Gate is safe for concurrent use and must not be copied after first use.
// Every method accepts a nil *Gate, which is name summon switched off: nothing
// is vetoed and the setters do nothing. A host can therefore call
// agent.NameSummon().SetBypass(true) without asking first whether it is on.
type Gate struct {
	matcher  Matcher
	agent    string
	followUp time.Duration
	now      func() time.Time

	bypass atomic.Bool

	mu         sync.Mutex
	repliedAt  time.Time
	hasReplied bool
}

// GateOption adjusts a Gate at construction.
type GateOption func(*Gate)

// WithFillers replaces the words accepted in front of a name. See
// Matcher.WithFillers.
func WithFillers(fillers ...string) GateOption {
	return func(g *Gate) { g.matcher = g.matcher.WithFillers(fillers) }
}

// WithFollowUp keeps the gate open for window after each ReplyEnded, so the
// answer to a question the agent just asked does not need the name again.
// Zero, the default, turns follow-ups off.
//
// The window is measured to the moment the gate is consulted, which is after
// the follow-up has been spoken and transcribed, so it has to cover the
// speaker's reaction time and the sentence itself. Speech from anyone in the
// room is let through while it is open.
func WithFollowUp(window time.Duration) GateOption {
	return func(g *Gate) { g.followUp = window }
}

// withClock is the test seam for the follow-up window.
func withClock(now func() time.Time) GateOption {
	return func(g *Gate) { g.now = now }
}

// NewGate builds the gate for agentName plus aliases, with the same naming
// rules as NewMatcher. It returns ErrNoName when no usable name is left.
func NewGate(agentName string, aliases []string, opts ...GateOption) (*Gate, error) {
	g := &Gate{
		matcher: NewMatcher(agentName, aliases),
		agent:   agentName,
		now:     time.Now,
	}
	for _, opt := range opts {
		opt(g)
	}
	names := g.matcher.Names()
	if len(names) == 0 {
		return nil, ErrNoName
	}
	if normalizeName(g.agent) == "" {
		// Only aliases were given; the rejection message still needs a name.
		g.agent = names[0]
	}
	return g, nil
}

// Names reports the wake words, longest first.
func (g *Gate) Names() []string {
	if g == nil {
		return nil
	}
	return g.matcher.Names()
}

// Addressed reports whether text opens with one of the agent's names. It is
// the matching rule alone: bypass and the follow-up window are not consulted.
func (g *Gate) Addressed(text string) bool {
	if g == nil {
		return false
	}
	return g.matcher.Addressed(text)
}

// Veto reports why a transcribed utterance must not open a turn. An empty
// string lets the turn run.
func (g *Gate) Veto(text string) string {
	if g == nil {
		return ""
	}
	if g.bypass.Load() || g.matcher.Addressed(text) || g.inFollowUp() {
		return ""
	}
	return "Not addressed to " + g.agent + " — ignored: “" + excerpt(text, 48) + "”"
}

// SetBypass stands the gate down while on is true. A host sets it for a turn
// the user opened by some other deliberate act: a held talk key, a button, a
// tap on the microphone. It stays set until cleared.
func (g *Gate) SetBypass(on bool) {
	if g == nil {
		return
	}
	g.bypass.Store(on)
}

// ReplyEnded records that the agent has just finished replying, which opens
// the follow-up window. It does nothing useful without WithFollowUp.
// voiceagent.New calls it from the event bus; a host that installs the gate by
// hand calls it itself.
func (g *Gate) ReplyEnded() {
	if g == nil {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.repliedAt = g.now()
	g.hasReplied = true
}

func (g *Gate) inFollowUp() bool {
	if g.followUp <= 0 {
		return false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.hasReplied && g.now().Sub(g.repliedAt) <= g.followUp
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
