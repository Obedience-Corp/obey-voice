package summon

import (
	"reflect"
	"testing"
)

func productRoster(t *testing.T, identities ...Identity) *Roster {
	t.Helper()
	r, err := NewRoster(identities)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestNamedOnlyInProductName(t *testing.T) {
	r := productRoster(t,
		Identity{ID: "obey", DisplayName: "Obey"},
		Identity{ID: "wren", DisplayName: "Wren"},
		Identity{ID: "voice", DisplayName: "Voice"},
	)
	for _, tc := range []struct {
		text, id string
		want     bool
	}{
		{text: "This is just a test of Obey voice.", id: "obey", want: true},
		{text: "obey-voice is slow today", id: "obey", want: true},
		{text: "OBEY_VOICE crashed", id: "obey", want: true},
		{text: "I installed Obey Voice and Obey Voice again", id: "obey", want: true},
		{text: "This is just a test of Obey voice.", id: "voice", want: true},
		{text: "Obey, how do you like Obey Voice?", id: "obey", want: false},
		{text: "How do you like Obey Voice, Obey?", id: "obey", want: false},
		{text: "Obey, voice memo please", id: "obey", want: false},
		{text: "Obey. Voice notes are off", id: "obey", want: false},
		{text: "Obey what do you think", id: "obey", want: false},
		{text: "Wren, Obey Voice is slow", id: "wren", want: false},
		{text: "Is ObeyVoice running?", id: "obey", want: true},
		{text: "Obey Voices are many", id: "obey", want: false},
	} {
		if got := r.NamedOnlyInProductName(tc.text, tc.id); got != tc.want {
			t.Errorf("NamedOnlyInProductName(%q, %q) = %v, want %v", tc.text, tc.id, got, tc.want)
		}
	}
}

func TestPersonaNamedAfterTheProductIsStillAddressed(t *testing.T) {
	r := productRoster(t, Identity{ID: "app", DisplayName: "Obey Voice"}, Identity{ID: "wren", DisplayName: "Wren"})
	text := "Obey Voice, what time is it?"
	if r.NamedOnlyInProductName(text, "app") {
		t.Fatal("the product-named persona's whole name counted as only the product name")
	}
	got, err := r.Resolve(text)
	want := Address{PersonaID: "app", Remainder: "what time is it?", Reason: ReasonVocative}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("Resolve(%q) = %#v, %v", text, got, err)
	}
}

func TestGrammarDoesNotReadTheProductNameAsAName(t *testing.T) {
	r := productRoster(t, Identity{ID: "obey", DisplayName: "Obey"}, Identity{ID: "wren", DisplayName: "Wren"})
	for _, text := range []string{"Obey-voice, what time is it?", "Obey-voice.", "Hey obey-voice, hi", "Obey Voice, what time is it?", "Hey Obey voice"} {
		got, err := r.Resolve(text)
		if err != nil || got.PersonaID != "" {
			t.Errorf("Resolve(%q) = %#v, %v", text, got, err)
		}
		if exact := r.ResolveForegroundControl(text); exact.Kind != ExactControlNone {
			t.Errorf("ResolveForegroundControl(%q) = %#v", text, exact)
		}
	}
	for text, want := range map[string]string{
		"Obey, voice memo please": ReasonVocative,
		"Obey, what time is it?":  ReasonVocative,
		"Obey what do you think":  ReasonImperative,
		"Hey Obey":                ReasonBareName,
	} {
		got, err := r.Resolve(text)
		if err != nil || got.PersonaID != "obey" || got.Reason != want {
			t.Errorf("Resolve(%q) = %#v, %v", text, got, err)
		}
	}
}
