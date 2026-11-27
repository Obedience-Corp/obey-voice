package summon

import (
	"errors"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"
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
// just finished replying. With WithAcousticEvidence, VetoHeard also lets
// through an utterance whose opening name the transcript got wrong.
//
// A Gate is safe for concurrent use and must not be copied after first use.
// Every method accepts a nil *Gate, which is name summon switched off: nothing
// is vetoed and the setters do nothing. A host can therefore call
// agent.NameSummon().SetBypass(true) without asking first whether it is on.
type Gate struct {
	matcher  Matcher
	agent    string
	followUp time.Duration
	acoustic time.Duration
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

// DefaultAcousticWindow is how far into an utterance a keyword spotter's hit
// may arrive and still count as the utterance opening with the name. A
// spotter reports a name some tenths of a second after it was spoken, in
// steps of about a third of a second, so the window has to cover one filler,
// the name and that lag, and no more. Measured on synthesized speech through
// voice detection with 300 ms of pre-roll: "Juno, ..." and "Hey Juno, ..."
// report by 0.8 to 1.2 s; a name that is the third word or later ("Okay then
// Juno", "We flew to Juneau") reports by 1.5 s or later. The default errs
// toward ignoring: a missed address costs a repeat, a false one makes the
// agent answer talk that was not meant for it.
const DefaultAcousticWindow = 1300 * time.Millisecond

// WithAcousticEvidence lets VetoHeard admit an utterance on the word of a
// keyword spotter when the transcript does not open with the name. It exists
// for names a recogniser misspells ("Juneau, what's the weather" for Juno).
// window is how early in the utterance the hit must arrive; pass
// DefaultAcousticWindow unless measurement says otherwise. Zero, the default,
// turns it off.
//
// A hit proves the name was said, not that the agent was addressed, so two
// more things must hold: the hit came inside window, and the transcript does
// not spell the name anywhere. A transcript that spells the name correctly was
// already judged by where the name stands, and "tell me about Juno" stays
// vetoed.
//
// What this lets through is whatever the spotter wrongly hears in the first
// word or two of an utterance, which includes a sentence that names the agent
// there while the recogniser misspells it ("Juneau is lovely in June").
// Measure the name before relying on it.
func WithAcousticEvidence(window time.Duration) GateOption {
	return func(g *Gate) { g.acoustic = window }
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
	return g.VetoHeard(text, "", 0)
}

// VetoHeard is Veto with what a keyword spotter heard in the same utterance:
// the name it matched and how far into the utterance the match arrived. It has
// the shape of pipeline.Pipeline.AcousticAddressGate. With no hit, or without
// WithAcousticEvidence, it decides exactly as Veto does.
func (g *Gate) VetoHeard(text, heardName string, heardBy time.Duration) string {
	if g == nil {
		return ""
	}
	if g.bypass.Load() || g.matcher.Addressed(text) || g.inFollowUp() || g.heardAtStart(text, heardName, heardBy) {
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

// heardAtStart is the acoustic rule documented on WithAcousticEvidence.
func (g *Gate) heardAtStart(text, heardName string, heardBy time.Duration) bool {
	if g.acoustic <= 0 || heardBy <= 0 || heardBy > g.acoustic {
		return false
	}
	heard := normalizeName(heardName)
	if heard == "" || strings.TrimSpace(text) == "" {
		return false
	}
	names := g.matcher.Names()
	known := false
	for _, name := range names {
		known = known || name == heard
	}
	return known && !mentions(strings.ToLower(text), names)
}

// mentions reports whether body holds one of names as whole words anywhere.
// A possessive counts: "Juno's orbit" is talk about Juno.
func mentions(body string, names []string) bool {
	notWord := func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }
	words := strings.FieldsFunc(body, notWord)
	for _, name := range names {
		want := strings.FieldsFunc(name, notWord)
		for i := 0; len(want) > 0 && i+len(want) <= len(words); i++ {
			if slices.Equal(words[i:i+len(want)], want) {
				return true
			}
		}
	}
	return false
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
