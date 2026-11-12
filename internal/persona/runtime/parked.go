package personaruntime

import (
	"slices"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
)

// maxParkedBrains bounds the provider sessions kept for personas that are not
// speaking. The least recently used one is retired first.
const maxParkedBrains = 4

// parkedBrain is a persona's provider kept while another persona speaks. It
// is reused only for the same binding in the same conversation.
type parkedBrain struct {
	personaID, bindingHash, epoch string
	provider                      brain.Provider
}

// parkedFor returns the parked provider for a persona's current binding. A
// parked provider of an older binding or conversation is retired instead.
// The provider stays parked until the selection that reuses it commits.
func (s *Selector) parkedFor(personaID, bindingHash, epoch string) brain.Provider {
	i := slices.IndexFunc(s.parked, func(p parkedBrain) bool { return p.personaID == personaID })
	if i < 0 {
		return nil
	}
	if parked := s.parked[i]; parked.bindingHash == bindingHash && parked.epoch == epoch {
		return parked.provider
	}
	s.retireParked(func(p parkedBrain) bool { return p.personaID == personaID })
	return nil
}

func (s *Selector) unpark(provider brain.Provider) {
	s.parked = slices.DeleteFunc(s.parked, func(p parkedBrain) bool { return p.provider == provider })
}

func (s *Selector) park(entry parkedBrain) {
	s.retireParked(func(p parkedBrain) bool { return p.personaID == entry.personaID })
	s.parked = append(s.parked, entry)
	if extra := len(s.parked) - maxParkedBrains; extra > 0 {
		for _, oldest := range s.parked[:extra] {
			s.opts.Pipeline.RetireBrain(oldest.provider)
		}
		s.parked = slices.Delete(s.parked, 0, extra)
	}
}

// retireParked releases the parked providers that match retire.
func (s *Selector) retireParked(retire func(parkedBrain) bool) {
	kept := s.parked[:0]
	for _, parked := range s.parked {
		if retire(parked) {
			s.opts.Pipeline.RetireBrain(parked.provider)
			continue
		}
		kept = append(kept, parked)
	}
	clear(s.parked[len(kept):])
	s.parked = kept
}

func retireAll(parkedBrain) bool { return true }
