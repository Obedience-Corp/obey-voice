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

// agent.NameSummon() is nil when name summon is off, and a host wired to a
// button must not have to check before every press.
func TestNilGateIsNameSummonSwitchedOff(t *testing.T) {
	var gate *Gate
	gate.SetBypass(true)
	gate.ReplyEnded()
	if gate.Veto("anything at all") != "" || gate.Addressed("Juno") || gate.Names() != nil {
		t.Fatal("a nil gate did something")
	}
}

func TestGateIsSafeForConcurrentUse(t *testing.T) {
	gate, _ := NewGate("Juno", nil, WithFollowUp(time.Second))
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range 200 {
			gate.ReplyEnded()
			gate.SetBypass(false)
		}
	}()
	for range 200 {
		gate.Veto("yes")
	}
	<-done
}

func TestGateAcousticEvidence(t *testing.T) {
	gate, err := NewGate("Juno", []string{"the computer"}, WithAcousticEvidence(DefaultAcousticWindow))
	if err != nil {
		t.Fatal(err)
	}
	early, late := 1100*time.Millisecond, 3*time.Second
	for _, tc := range []struct {
		why, text, heard string
		by               time.Duration
		admitted         bool
	}{
		{"misspelled opening name", "Juneau, what's the weather?", "Juno", early, true},
		{"name heard as two words", "You know, set a timer.", "juno", early, true},
		{"alias misheard", "The commuter, lights off.", "The Computer", early, true},
		{"hit exactly at the window", "Juneau, hello.", "Juno", DefaultAcousticWindow, true},
		{"spelled mention, hit early", "Tell me about Juno.", "Juno", early, false},
		{"spelled possessive", "I think Juno's orbit is odd.", "Juno", early, false},
		{"spelled alias mention", "I bought the computer yesterday.", "the computer", early, false},
		{"misspelled name late in the utterance", "Yesterday my sister asked about Juneau.", "Juno", late, false},
		{"no hit", "Juneau, what's the weather?", "", 0, false},
		{"hit without a time", "Juneau, what's the weather?", "Juno", 0, false},
		{"hit for a name that is not ours", "Oh bay, what's the weather?", "Obey", early, false},
		{"empty transcript", "  ", "Juno", early, false},
		{"transcript opens with the name anyway", "Juno, what's the weather?", "", 0, true},
	} {
		if got := gate.VetoHeard(tc.text, tc.heard, tc.by) == ""; got != tc.admitted {
			t.Errorf("%s: %q heard %q by %s: admitted=%v, want %v", tc.why, tc.text, tc.heard, tc.by, got, tc.admitted)
		}
	}
}

func TestGateIgnoresAcousticEvidenceByDefault(t *testing.T) {
	gate, err := NewGate("Juno", nil)
	if err != nil {
		t.Fatal(err)
	}
	if gate.VetoHeard("Juneau, what's the weather?", "Juno", time.Second) == "" {
		t.Fatal("a gate without WithAcousticEvidence admitted on a spotter hit")
	}
	if gate.VetoHeard("Juno, what's the weather?", "", 0) != "" || gate.Veto("Juno, hi") != "" {
		t.Fatal("VetoHeard must decide as Veto does when the transcript opens with the name")
	}
	var none *Gate
	if none.VetoHeard("anything", "Juno", time.Second) != "" {
		t.Fatal("a nil gate vetoed")
	}
}

func TestGateWakeWindow(t *testing.T) {
	now := time.Unix(1000, 0)
	gate, _ := NewGate("Juno", nil, WithWake(6*time.Second), withClock(func() time.Time { return now }))

	if gate.Awake() || gate.Veto("what's the weather") == "" {
		t.Fatal("the gate was open before any wake word")
	}
	gate.Woke()
	now = now.Add(6 * time.Second)
	if reason := gate.Veto("what's the weather"); reason != "" || !gate.Awake() {
		t.Fatalf("a request inside the wake window was vetoed: %q", reason)
	}
	// The window is time, not a count: a second sentence inside it passes too.
	if gate.VetoHeard("and tomorrow", "", 0) != "" {
		t.Fatal("the wake window closed after one utterance")
	}
	now = now.Add(time.Millisecond)
	if gate.Awake() || gate.Veto("what's the weather") == "" {
		t.Fatal("the wake window never closed")
	}
	gate.Woke()
	if gate.Veto("what's the weather") != "" {
		t.Fatal("a later wake word did not reopen the window")
	}
}

func TestGateWakeIsOffByDefault(t *testing.T) {
	// A frozen clock: with no window, not even the instant of the wake counts.
	now := time.Unix(1000, 0)
	gate, _ := NewGate("Juno", nil, withClock(func() time.Time { return now }))
	gate.Woke()
	if gate.Awake() || gate.Veto("what's the weather") == "" {
		t.Fatal("a gate with no wake window was opened by Woke")
	}
	var none *Gate
	none.Woke()
	if none.Awake() {
		t.Fatal("a nil gate reported a wake")
	}
}

// "Juno" ... "what's the weather": the name by itself opens the window and is
// not sent to the model, so the request does not land on top of a reply.
func TestGateWakeWindowTakesTheBareNameAsAWake(t *testing.T) {
	now := time.Unix(1000, 0)
	gate, _ := NewGate("Juno", []string{"the computer"}, WithWake(6*time.Second), withClock(func() time.Time { return now }))

	for _, alone := range []string{"Juno.", "juno", "Hey Juno?", "  \"Juno!\"", "The computer."} {
		gate.wokeAt, gate.hasWoken = time.Time{}, false
		reason := gate.Veto(alone)
		if !strings.Contains(reason, "waiting for the request") || !gate.Awake() {
			t.Errorf("%q: reason %q, awake %v; want a veto that opens the window", alone, reason, gate.Awake())
		}
	}
	if gate.Veto("what's the weather") != "" {
		t.Fatal("the request after the bare name was vetoed")
	}
	// A request in the same breath is answered as before.
	now = now.Add(time.Hour)
	for _, request := range []string{"Juno, what's the weather", "Hey Juno stop", "Juno's orbit is long"} {
		if reason := gate.Veto(request); strings.Contains(reason, "waiting") {
			t.Errorf("%q was taken for the bare name", request)
		}
	}
	if gate.Veto("Juno, what's the weather") != "" {
		t.Fatal("an addressed request was vetoed")
	}
	// A held talk key means the turn is addressed, whatever it says.
	gate.SetBypass(true)
	if gate.Veto("Juno.") != "" {
		t.Fatal("bypass did not let the bare name through")
	}
	// And with no wake window the bare name is an ordinary addressed utterance.
	plain, _ := NewGate("Juno", nil)
	if plain.Veto("Juno.") != "" {
		t.Fatal("a gate with no wake window stopped answering its name")
	}
}
