package events

import "time"

// NameHeard reports that the live keyword listener heard one of the agent's
// names on the microphone, while the user may still be speaking. It is the
// cue for a chime or a light, and arrives before any transcript.
//
// It is not a decision to answer. The listener also fires on a name inside a
// sentence ("tell me about Juno") and on words that sound like it; whether the
// utterance opens a turn is still decided by the name summon gate. Emitted
// only by an agent built with voiceagent.Options.NameSummonLive.
type NameHeard struct {
	// Name is one of Agent.NameSummon().Names(): the configured name in the
	// gate's lower-case form, not a recogniser's spelling of what was said.
	Name string
	// At is when the hit was reported, a few tenths of a second after the
	// name was spoken.
	At time.Time
}

func (NameHeard) eventType() string { return "name_heard" }
