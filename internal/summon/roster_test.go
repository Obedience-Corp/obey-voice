package summon

import (
	"errors"
	"reflect"
	"testing"
)

func testRoster(t *testing.T) *Roster {
	t.Helper()
	r, err := NewRoster([]Identity{
		{ID: "santa", DisplayName: "Santa", Aliases: []string{"Saint Nick"}},
		{ID: "samantha", DisplayName: "Samantha", Aliases: []string{"Sam"}},
		{ID: "uncle", DisplayName: "Uncle Fu"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestRosterResolve(t *testing.T) {
	r := testRoster(t)
	tests := []struct {
		name string
		text string
		want Address
	}{
		{name: "bare", text: "Santa", want: Address{PersonaID: "santa", Reason: ReasonBareName, Bare: true}},
		{name: "vocative preserves remainder case", text: "Santa, Please help", want: Address{PersonaID: "santa", Remainder: "Please help", Reason: ReasonVocative}},
		{name: "punctuationless imperative", text: "Samantha tell me a story", want: Address{PersonaID: "samantha", Remainder: "tell me a story", Reason: ReasonImperative}},
		{name: "filler", text: "OK, Santa, status", want: Address{PersonaID: "santa", Remainder: "status", Reason: ReasonVocative}},
		{name: "longest name", text: "Uncle Fu, hello", want: Address{PersonaID: "uncle", Remainder: "hello", Reason: ReasonVocative}},
		{name: "generated first word", text: "Uncle, hello", want: Address{PersonaID: "uncle", Remainder: "hello", Reason: ReasonVocative}},
		{name: "alias", text: "Saint Nick, hello", want: Address{PersonaID: "santa", Remainder: "hello", Reason: ReasonVocative}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := r.Resolve(tt.text)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("Resolve(%q) = %#v, want %#v", tt.text, got, tt.want)
			}
		})
	}
}

func TestRosterResolveLeavesIncidentalAndDeclarativeTextAlone(t *testing.T) {
	r := testRoster(t)
	for _, text := range []string{"I told Santa earlier", "Santa is a persona", "Santa and Samantha are here", "unknownfirstword is ordinary speech"} {
		got, err := r.Resolve(text)
		if err != nil {
			t.Fatalf("Resolve(%q) error = %v", text, err)
		}
		want := Address{Remainder: text, Reason: ReasonNoAddress}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("Resolve(%q) = %#v, want %#v", text, got, want)
		}
	}
}

func TestRosterUnknownExplicitSwitch(t *testing.T) {
	r := testRoster(t)
	text := "switch to Unknown"
	got, err := r.Resolve(text)
	var unknown *UnknownTargetError
	if !errors.As(err, &unknown) || unknown.Target != "Unknown" {
		t.Fatalf("Resolve(%q) error = %v, target %#v", text, err, unknown)
	}
	if want := (Address{Remainder: text, Reason: ReasonUnknownTarget}); !reflect.DeepEqual(got, want) {
		t.Fatalf("Resolve(%q) = %#v, want %#v", text, got, want)
	}
}

func TestNewRosterRejectsAliasCollision(t *testing.T) {
	_, err := NewRoster([]Identity{
		{ID: "santa", DisplayName: "Santa"},
		{ID: "samantha", DisplayName: "Samantha", Aliases: []string{"SANTA"}},
	})
	if err == nil {
		t.Fatal("NewRoster accepted a normalized alias collision")
	}
}

func TestRosterUnicodeAndPunctuationBoundaries(t *testing.T) {
	r, err := NewRoster([]Identity{{ID: "santa", DisplayName: "Sánta"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"SÁNTA， What now", "Sánta—tell me"} {
		got, err := r.Resolve(text)
		if err != nil || got.PersonaID != "santa" {
			t.Errorf("Resolve(%q) = %#v, %v; want santa", text, got, err)
		}
	}
	if got, err := r.Resolve("Sánta's story"); err != nil || got.PersonaID != "" {
		t.Fatalf("possessive mention Resolve = %#v, %v; want no address", got, err)
	}
}

func TestEmptyRosterStillRejectsExplicitUnknownTarget(t *testing.T) {
	r, err := NewRoster(nil)
	if err != nil {
		t.Fatal(err)
	}
	text := "switch to Unknown"
	got, err := r.Resolve(text)
	var unknown *UnknownTargetError
	if !errors.As(err, &unknown) || got.PersonaID != "" || got.Remainder != text {
		t.Fatalf("Resolve(%q) = %#v, %v; want unknown target with original remainder", text, got, err)
	}
}

func TestRosterKeepsNameLeadingDescriptions(t *testing.T) {
	r, err := NewRoster([]Identity{{ID: "santa", DisplayName: "Santa"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"Santa lives at the North Pole", "Santa knows my name", "Santa can fly", "Santa will visit tomorrow"} {
		got, err := r.Resolve(text)
		if err != nil || got.PersonaID != "" || got.Remainder != text {
			t.Fatalf("declarative %q selected %+v: %v", text, got, err)
		}
	}
}
