package summon

import (
	"slices"
	"strings"
	"testing"
)

func TestMatcherAddressed(t *testing.T) {
	tests := []struct {
		name      string
		agentName string
		aliases   []string
		text      string
		want      bool
	}{
		{name: "bare name opens the sentence", agentName: "Obey", text: "obey what's the time", want: true},
		{name: "filler then name then comma", agentName: "Obey", text: "Hey Obey, what's the time?", want: true},
		{name: "filler with a comma of its own", agentName: "Obey", text: "OK, Obey — dinner?", want: true},
		{name: "case is ignored", agentName: "Obey", text: "OBEY, stop", want: true},
		{name: "name alone", agentName: "Obey", text: "Obey", want: true},
		{name: "name then a full stop", agentName: "Obey", text: "Obey. Play something.", want: true},
		{name: "leading quote does not hide the name", agentName: "Obey", text: `"Obey, are you there?"`, want: true},
		{name: "every filler is accepted", agentName: "Obey", text: "yo obey, status", want: true},
		{name: "multi-word display name", agentName: "Uncle Fu", text: "uncle fu, you're late", want: true},
		{name: "first word of a multi-word name", agentName: "Uncle Fu", text: "uncle, you're late", want: true},
		{name: "alias", agentName: "Obey", aliases: []string{"computer"}, text: "Computer, lights off", want: true},
		{name: "alias with a filler", agentName: "Obey", aliases: []string{"computer"}, text: "hey computer, lights off", want: true},

		{name: "name mid-sentence", agentName: "Obey", text: "I told obey earlier", want: false},
		{name: "name is only a prefix of the first word", agentName: "Obey", text: "obeying the rules", want: false},
		{name: "no name at all", agentName: "Obey", text: "what's for dinner?", want: false},
		{name: "empty transcript", agentName: "Obey", text: "", want: false},
		{name: "whitespace only", agentName: "Obey", text: "   ", want: false},
		{name: "two fillers is talk about the agent", agentName: "Obey", text: "hey there obey", want: false},
		{name: "a filler is not itself an address", agentName: "Obey", text: "hey, what's for dinner?", want: false},
		{name: "second word of a multi-word name is not a name", agentName: "Uncle Fu", text: "fu, you're late", want: false},
		{name: "an alias does not match mid-sentence", agentName: "Obey", aliases: []string{"computer"}, text: "my computer is slow", want: false},
		{name: "no name configured never matches", agentName: "", text: "obey what's the time", want: false},
		{name: "no name configured ignores even an empty utterance", agentName: "   ", text: "", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := NewMatcher(tt.agentName, tt.aliases)
			if got := m.Addressed(tt.text); got != tt.want {
				t.Errorf("Addressed(%q) with names %v = %v, want %v", tt.text, m.Names(), got, tt.want)
			}
		})
	}
}

// The zero Matcher is what a caller gets from a struct literal; it must be as
// deaf as one built from an empty name.
func TestZeroMatcherNeverMatches(t *testing.T) {
	var m Matcher
	if m.Addressed("obey, hello") {
		t.Error("the zero Matcher answered to a name")
	}
	if names := m.Names(); len(names) != 0 {
		t.Errorf("Names() = %v, want none", names)
	}
}

func TestNewMatcherNames(t *testing.T) {
	m := NewMatcher("  Uncle   Fu ", []string{"computer", "UNCLE", "", "  ", "obey"})
	got := m.Names()
	want := []string{"computer", "uncle fu", "uncle", "obey"}
	if !slices.Equal(got, want) {
		t.Fatalf("Names() = %v, want %v", got, want)
	}
	// Longest first is what keeps "uncle fu" from being shadowed by "uncle".
	for i := 1; i < len(got); i++ {
		if len(got[i]) > len(got[i-1]) {
			t.Fatalf("names not sorted longest first: %v", got)
		}
	}
}

// Names hands out a copy: a caller joining it for the activity panel must not
// be able to reorder the matcher underneath the pipeline.
func TestNamesIsACopy(t *testing.T) {
	m := NewMatcher("Obey", []string{"computer"})
	names := m.Names()
	slices.Reverse(names)
	names[0] = "clobbered"
	if !m.Addressed("computer, hello") {
		t.Error("mutating the Names() result changed what the matcher answers to")
	}
}

// Every documented filler has to work, or the docs are lying about the list.
func TestEveryFillerIsAccepted(t *testing.T) {
	m := NewMatcher("Obey", nil)
	for _, filler := range Fillers {
		if !m.Addressed(filler + " obey, status") {
			t.Errorf("filler %q was not accepted", filler)
		}
		// A filler glued to the name is one word, not a filler plus a name.
		if m.Addressed(filler + "obey status") {
			t.Errorf("filler %q matched without a word break", filler)
		}
	}
}

func TestAddressedIgnoresSurroundingCaseAndSpace(t *testing.T) {
	m := NewMatcher("Obey", nil)
	for _, text := range []string{"  Obey, hello", "\tobey hello", strings.ToUpper("hey obey hello")} {
		if !m.Addressed(text) {
			t.Errorf("Addressed(%q) = false, want true", text)
		}
	}
}
