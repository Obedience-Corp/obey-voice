package summon

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestGateLetsOnlyAddressedUtterancesThrough(t *testing.T) {
	gate, err := NewGate("Juno", []string{"computer"})
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"Juno, what time is it?", "hey juno what time is it", "Computer, lights off."} {
		if reason := gate.Veto(text); reason != "" {
			t.Errorf("Veto(%q) = %q, want the turn to run", text, reason)
		}
	}
	reason := gate.Veto("what's for dinner tonight?")
	if !strings.Contains(reason, "Juno") || !strings.Contains(reason, "what's for dinner") {
		t.Errorf("rejection %q does not name the agent and the utterance", reason)
	}
	if got := gate.Names(); !slices.Equal(got, []string{"computer", "juno"}) {
		t.Errorf("Names() = %v", got)
	}
}

func TestGateRefusesToExistWithoutAName(t *testing.T) {
	for _, aliases := range [][]string{nil, {"", "   "}} {
		if gate, err := NewGate("  ", aliases); !errors.Is(err, ErrNoName) || gate != nil {
			t.Errorf("NewGate(blank, %q) = %v, %v; want ErrNoName", aliases, gate, err)
		}
	}
}

func TestGateNamedOnlyByAliasStillNamesItselfInTheRejection(t *testing.T) {
	gate, err := NewGate("", []string{"Computer"})
	if err != nil {
		t.Fatal(err)
	}
	if reason := gate.Veto("turn it up"); !strings.Contains(reason, "Not addressed to computer") {
		t.Errorf("rejection = %q", reason)
	}
}

func TestGateBypassStandsTheGateDownUntilCleared(t *testing.T) {
	gate, _ := NewGate("Juno", nil)
	gate.SetBypass(true)
	if reason := gate.Veto("turn it up"); reason != "" {
		t.Fatalf("bypassed gate vetoed: %q", reason)
	}
	gate.SetBypass(false)
	if gate.Veto("turn it up") == "" {
		t.Fatal("the gate stayed open after the bypass was cleared")
	}
}

func TestGateFollowUpWindow(t *testing.T) {
	now := time.Unix(1000, 0)
	gate, _ := NewGate("Juno", nil, WithFollowUp(8*time.Second), withClock(func() time.Time { return now }))

	if gate.Veto("yes") == "" {
		t.Fatal("a follow-up was accepted before the agent ever replied")
	}
	gate.ReplyEnded()
	now = now.Add(8 * time.Second)
	if reason := gate.Veto("yes"); reason != "" {
		t.Fatalf("follow-up inside the window was vetoed: %q", reason)
	}
	now = now.Add(time.Millisecond)
	if gate.Veto("yes") == "" {
		t.Fatal("the follow-up window never closed")
	}
	// An unanswered utterance must not hold the window open: only a reply does.
	gate.ReplyEnded()
	now = now.Add(time.Second)
	if gate.Veto("the second one") != "" {
		t.Fatal("a later reply did not reopen the window")
	}
}

func TestGateFollowUpIsOffByDefault(t *testing.T) {
	gate, _ := NewGate("Juno", nil)
	gate.ReplyEnded()
	if gate.Veto("yes") == "" {
		t.Fatal("a gate with no follow-up window accepted a nameless follow-up")
	}
}

func TestGateFillersCanBeReplaced(t *testing.T) {
	gate, _ := NewGate("Juno", nil, WithFillers("oye"))
	if gate.Veto("oye Juno, pon música") != "" {
		t.Error("the replacement filler was not accepted")
	}
	if gate.Veto("hey Juno, play music") == "" {
		t.Error("a default filler survived being replaced")
	}
	bare, _ := NewGate("Juno", nil, WithFillers())
	if bare.Veto("hey Juno") == "" || bare.Veto("Juno, hello") != "" {
		t.Error("an empty filler list should require the bare name")
	}
}

func TestDefaultFillersIsACopy(t *testing.T) {
	DefaultFillers()[0] = "changed"
	if DefaultFillers()[0] != "hey" {
		t.Fatal("a caller rewrote the package's filler list")
	}
}

func TestGateRejectionExcerptCountsRunes(t *testing.T) {
	gate, _ := NewGate("Juno", nil)
	reason := gate.Veto(strings.Repeat("é", 80))
	if !strings.Contains(reason, strings.Repeat("é", 47)+"…") {
		t.Errorf("rejection = %q", reason)
	}
}
