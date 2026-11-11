package personaruntime

import (
	"fmt"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/pipeline"
)

func TestParkedBrainsRetireLeastRecentlyUsedPastTheCap(t *testing.T) {
	p := &pipeline.Pipeline{}
	s := &Selector{opts: SelectionOptions{Pipeline: p}}
	var brains []*selectorTestBrain
	park := func(i int) {
		s.park(parkedBrain{personaID: fmt.Sprint("p", i), bindingHash: "h", epoch: "e", provider: brains[i]})
	}
	for i := range maxParkedBrains + 1 {
		brains = append(brains, newSelectorTestBrain("", nil))
		park(i)
	}
	// p1 speaks and is parked again, so p2 is now the least recently used.
	if got := s.parkedFor("p1", "h", "e"); got != brains[1] {
		t.Fatalf("parked p1 = %v", got)
	}
	s.unpark(brains[1])
	park(1)
	brains = append(brains, newSelectorTestBrain("", nil))
	park(len(brains) - 1)
	if err := p.CloseBrains(); err != nil {
		t.Fatal(err)
	}
	for i, b := range brains {
		if want := i == 0 || i == 2; b.IsClosed() != want {
			t.Fatalf("brain %d closed = %v, want %v", i, b.IsClosed(), want)
		}
	}
	if len(s.parked) != maxParkedBrains {
		t.Fatalf("parked = %d, want %d", len(s.parked), maxParkedBrains)
	}

	if got := s.parkedFor("p3", "edited", "e"); got != nil {
		t.Fatalf("a parked brain was reused for another binding: %v", got)
	}
	if got := s.parkedFor("p4", "h", "cleared"); got != nil {
		t.Fatalf("a parked brain was reused in another conversation: %v", got)
	}
	s.retireParked(retireAll)
	if err := p.CloseBrains(); err != nil {
		t.Fatal(err)
	}
	for i, b := range brains {
		if !b.IsClosed() {
			t.Fatalf("brain %d was not retired", i)
		}
	}
	if len(s.parked) != 0 {
		t.Fatalf("parked after retiring all = %d", len(s.parked))
	}
}

func TestSwitchingAwayFromClaudeParksItsSessionUntilClear(t *testing.T) {
	c := newClaudeConversation(t, 20)
	c.say("Note that the vault code is cobalt.", "switch to Ember")
	if len(c.selector.parked) != 1 || c.selector.parked[0].personaID != "mira" || c.selector.parked[0].provider != c.initial {
		t.Fatalf("parked = %+v", c.selector.parked)
	}
	if err := c.p.ClearConversation(); err != nil {
		t.Fatal(err)
	}
	c.say("Start over.")
	if len(c.selector.parked) != 0 {
		t.Fatalf("a cleared conversation kept a parked session: %+v", c.selector.parked)
	}

	closing := newClaudeConversation(t, 20)
	closing.say("Note that the vault code is cobalt.", "switch to Ember")
	if len(closing.selector.parked) != 1 {
		t.Fatalf("parked = %+v", closing.selector.parked)
	}
	closing.selector.Close()
	if len(closing.selector.parked) != 0 {
		t.Fatalf("closing selection kept a parked session: %+v", closing.selector.parked)
	}
}
