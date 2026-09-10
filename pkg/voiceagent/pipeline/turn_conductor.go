package pipeline

import (
	"fmt"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

// turnConductor bridges the turn state machine into the live pipeline: stages
// report state transitions and it guarantees exactly one terminal TurnMetrics
// emission per turn, regardless of which return path ends the turn.
type turnConductor struct {
	p        *Pipeline
	machine  *turnMachine
	metrics  *turnMetrics
	finished bool
}

func (p *Pipeline) newTurnConductor(metrics *turnMetrics) *turnConductor {
	return &turnConductor{p: p, machine: newTurnMachine(), metrics: metrics}
}

// to advances the lifecycle state, ignoring illegal or out-of-order transitions
// so a late stage signal cannot corrupt the turn outcome.
func (c *turnConductor) to(state TurnState) { c.machine.To(state) }

// finish moves the machine to its terminal state and emits the single terminal
// metrics event. Later calls are no-ops, so every return path can call it.
func (c *turnConductor) finish(terminal TurnState) {
	if c.finished {
		return
	}
	c.finished = true
	c.machine.To(terminal)
	// The machine is the outcome authority: the emitted metrics carry the
	// terminal state it settled on. A rejected transition (late or illegal
	// signal) falls back to the caller's intent so the outcome is never a
	// non-terminal state.
	outcome, ok := c.machine.Terminal()
	if !ok {
		outcome = terminal
	}
	m := c.metrics.snapshot()
	m.Outcome = outcome.String()
	c.p.emit(m)
	c.p.warnIfSynthesisCannotKeepUp(m)
}

// underrunDeadAirShare is the fraction of a turn's speaking time that may be
// silence before the synthesizer is judged unable to keep up. A little slack
// between sentences is normal; a third of the turn is a different engine.
const underrunDeadAirShare = 0.25

// warnIfSynthesisCannotKeepUp says so once per session when a turn spent a
// large share of its speaking time in silence.
//
// It is deliberately not a per-turn message: when an engine is too slow for
// conversation it is too slow on every turn, and a warning that fires every
// time is one people learn to ignore. It is also deliberately not an
// automatic downgrade — which voice to use is the operator's call, not a
// decision to make silently on their behalf mid-conversation.
//
// It fires on either of two symptoms of the same illness. Silence between
// sentences is what a whole-utterance engine does when it falls behind. Gaps
// inside a sentence are what an incrementally-played one does, and those are
// worse — the reply stutters mid-word — so a single one is enough to say so
// rather than a quarter of the turn.
func (p *Pipeline) warnIfSynthesisCannotKeepUp(m events.TurnMetrics) {
	if m.Interrupted {
		return
	}
	// Kokoro has no faster-voice remedy here. Keep its raw timing metrics,
	// but do not turn gaps into this advice. Fallback turns also include
	// Kokoro audio, even though TTSProvider still names the primary engine;
	// their aggregate timings cannot justify advice about that primary.
	if m.TTSProvider == "kokoro" || m.TTSFallbacks > 0 {
		return
	}
	speaking := m.SpeakingWindow()
	deadAirShare := m.DeadAirElapsed > 0 && speaking > 0 &&
		float64(m.DeadAirElapsed)/float64(speaking) >= underrunDeadAirShare
	if !deadAirShare && m.Underruns == 0 {
		return
	}
	engine := m.TTSProvider
	if engine == "" {
		engine = "the speech engine"
	}
	if m.TTSTier != "" {
		engine += " " + m.TTSTier
	}
	symptom := fmt.Sprintf("%s of that reply's %s speaking time was silence between sentences",
		m.DeadAirElapsed.Round(time.Second), speaking.Round(time.Second))
	remedy := "A faster voice (Settings → TTS) removes the gaps."
	if m.Underruns > 0 {
		symptom = fmt.Sprintf("that reply broke up %s while it was still being generated",
			pluralize(m.Underruns, "time", "times"))
		remedy = "Set qwen_tts_stream to off to speak whole sentences instead, or use a faster voice (Settings → TTS)."
	}
	p.underrunOnce.Do(func() {
		p.emit(events.Info{Message: fmt.Sprintf(
			"%s is synthesizing slower than it plays: %s. %s", engine, symptom, remedy)})
	})
}

func pluralize(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}
