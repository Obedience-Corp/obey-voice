package summon

import (
	"errors"
	"reflect"
	"slices"
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

func TestRosterRefusesOnlyAddressedCollisions(t *testing.T) {
	identities := []Identity{
		{ID: "veronica", DisplayName: "Veronica"},
		{ID: "veronica-hype", DisplayName: "Veronica Hype"},
		{ID: "samantha", DisplayName: "Samantha", Aliases: []string{"VERONICA", "Veronica"}},
	}
	for _, reverse := range []bool{false, true} {
		if reverse {
			slices.Reverse(identities)
		}
		r, err := NewRoster(identities)
		if err != nil {
			t.Fatal(err)
		}
		for _, text := range []string{"Veronica", "VERONICA!", "Hey, Veronica, help", "Veronica tell me a joke", "switch to Veronica"} {
			got, err := r.Resolve(text)
			var ambiguous *AmbiguousTargetError
			if !errors.As(err, &ambiguous) || ambiguous.Target != "veronica" || !reflect.DeepEqual(ambiguous.PersonaIDs, []string{"samantha", "veronica", "veronica-hype"}) {
				t.Fatalf("Resolve(%q) = %+v, %v; want deterministic ambiguity", text, got, err)
			}
			if got.PersonaID != "" || got.Remainder != text || got.Reason != ReasonAmbiguousTarget {
				t.Fatalf("ambiguous address = %+v", got)
			}
		}
		for _, text := range []string{"tell me a joke", "Veronica is a persona", "I told Veronica earlier", "Veronica's story"} {
			got, err := r.Resolve(text)
			if err != nil || got.PersonaID != "" || got.Remainder != text {
				t.Fatalf("unaddressed Resolve(%q) = %+v, %v", text, got, err)
			}
		}
		for text, id := range map[string]string{"Samantha, hello": "samantha", "Veronica Hype, hello": "veronica-hype", "switch to Veronica Hype": "veronica-hype"} {
			got, err := r.Resolve(text)
			if err != nil || got.PersonaID != id {
				t.Fatalf("unique Resolve(%q) = %+v, %v; want %s", text, got, err, id)
			}
		}
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

func TestRosterResolveExactControlRecognizesOnlyWholeInputControls(t *testing.T) {
	r := testRoster(t)
	tests := []struct {
		name       string
		text       string
		wantKind   ExactControlKind
		wantID     string
		wantReason string
		wantErr    any
	}{
		{name: "bare", text: "Santa", wantKind: ExactControlValid, wantID: "santa", wantReason: ReasonBareName},
		{name: "bare with punctuation", text: " Santa... ", wantKind: ExactControlValid, wantID: "santa", wantReason: ReasonBareName},
		{name: "greeting", text: "Hey Santa", wantKind: ExactControlValid, wantID: "santa", wantReason: ReasonBareName},
		{name: "switch", text: "switch to Santa", wantKind: ExactControlValid, wantID: "santa", wantReason: ReasonExplicitSwitch},
		{name: "quoted is excluded", text: `"Santa"`, wantKind: ExactControlNone},
		{name: "single-quoted switch target is semantic", text: "switch to 'Santa'", wantKind: ExactControlNone},
		{name: "substantive vocative tail", text: "Santa, tell me a story", wantKind: ExactControlNone},
		{name: "switch with substantive tail", text: "switch to Santa and tell me a story", wantKind: ExactControlNone},
		{name: "unknown bare is semantic", text: "Unknown", wantKind: ExactControlNone},
		{name: "unknown switch refuses", text: "switch to Unknown", wantKind: ExactControlInvalid, wantErr: (*UnknownTargetError)(nil)},
		{name: "unknown switch with punctuation refuses", text: "switch to Unknown!", wantKind: ExactControlInvalid, wantErr: (*UnknownTargetError)(nil)},
		{name: "unknown switch with substantive tail is semantic", text: "switch to Unknown and tell me a story", wantKind: ExactControlNone},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := r.ResolveExactControl(tt.text)
			if got.Kind != tt.wantKind {
				t.Fatalf("ResolveExactControl(%q).Kind = %v, want %v", tt.text, got.Kind, tt.wantKind)
			}
			if got.Address.PersonaID != tt.wantID || (tt.wantReason != "" && got.Address.Reason != tt.wantReason) {
				t.Fatalf("ResolveExactControl(%q) address = %+v", tt.text, got.Address)
			}
			if tt.wantErr == nil {
				if got.Err != nil {
					t.Fatalf("ResolveExactControl(%q) err = %v", tt.text, got.Err)
				}
				return
			}
			switch tt.wantErr.(type) {
			case *UnknownTargetError:
				var unknown *UnknownTargetError
				if !errors.As(got.Err, &unknown) {
					t.Fatalf("ResolveExactControl(%q) err = %v, want unknown target", tt.text, got.Err)
				}
			default:
				t.Fatalf("test bug: unsupported expected error type %T", tt.wantErr)
			}
		})
	}
}

func TestRosterResolveExactControlAliasPrecedenceAndAmbiguity(t *testing.T) {
	r, err := NewRoster([]Identity{
		{ID: "santa", DisplayName: "Santa"},
		{ID: "helper", DisplayName: "Helper", Aliases: []string{"Hey Santa", "switch to Santa"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	for text, want := range map[string]string{
		"Hey Santa":       "helper",
		"switch to Santa": "helper",
	} {
		got := r.ResolveExactControl(text)
		if got.Kind != ExactControlValid || got.Address.PersonaID != want || got.Err != nil {
			t.Fatalf("ResolveExactControl(%q) = %+v, want helper alias winner", text, got)
		}
	}

	ambiguous, err := NewRoster([]Identity{
		{ID: "santa", DisplayName: "Santa"},
		{ID: "helper-a", DisplayName: "Helper A", Aliases: []string{"Hey Santa"}},
		{ID: "helper-b", DisplayName: "Helper B", Aliases: []string{"Hey Santa"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := ambiguous.ResolveExactControl("Hey Santa")
	var collision *AmbiguousTargetError
	if got.Kind != ExactControlInvalid || !errors.As(got.Err, &collision) {
		t.Fatalf("ambiguous alias result = %+v, want exact invalid ambiguity", got)
	}
}

func TestRosterResolveExactControlPunctuatedAliasPrecedenceAndAmbiguity(t *testing.T) {
	cases := []struct {
		name  string
		alias string
	}{
		{name: "direct punctuation", alias: "Santa!"},
		{name: "greeting punctuation", alias: "Hey Santa!"},
		{name: "switch punctuation", alias: "switch to Santa!"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, err := NewRoster([]Identity{
				{ID: "santa", DisplayName: "Santa"},
				{ID: "helper", DisplayName: "Helper", Aliases: []string{tc.alias}},
			})
			if err != nil {
				t.Fatal(err)
			}
			for _, input := range []string{tc.alias, tc.alias + "!"} {
				got := r.ResolveExactControl(input)
				if got.Kind != ExactControlValid || got.Address.PersonaID != "helper" || got.Address.Reason != ReasonBareName || got.Err != nil {
					t.Fatalf("precedence ResolveExactControl(%q) = %+v", input, got)
				}
			}

			ambiguous, err := NewRoster([]Identity{
				{ID: "santa", DisplayName: "Santa"},
				{ID: "helper-a", DisplayName: "Helper A", Aliases: []string{tc.alias}},
				{ID: "helper-b", DisplayName: "Helper B", Aliases: []string{tc.alias}},
			})
			if err != nil {
				t.Fatal(err)
			}
			for _, input := range []string{tc.alias, tc.alias + "!"} {
				got := ambiguous.ResolveExactControl(input)
				var collision *AmbiguousTargetError
				if got.Kind != ExactControlInvalid || !errors.As(got.Err, &collision) || got.Address.PersonaID != "" || got.Address.Remainder != input || got.Address.Reason != ReasonAmbiguousTarget {
					t.Fatalf("ambiguity ResolveExactControl(%q) = %+v", input, got)
				}
			}
		})
	}
}

func TestRosterResolveExactControlCoordinatorPunctuatedAliasOnly(t *testing.T) {
	r, err := NewRoster([]Identity{
		{ID: "santa", DisplayName: "Santa"},
		{ID: "helper", DisplayName: "Helper", Aliases: []string{"Santa!"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	for text, wantReason := range map[string]string{
		"Santa!":            ReasonBareName,
		"Hey Santa!":        ReasonBareName,
		"switch to Santa!":  ReasonExplicitSwitch,
		"Santa!!":           ReasonBareName,
		"Santa!?":           ReasonBareName,
		"Santa!\u2026":      ReasonBareName,
		"Hey Santa!!":       ReasonBareName,
		"switch to Santa!!": ReasonExplicitSwitch,
	} {
		got := r.ResolveExactControl(text)
		if got.Kind != ExactControlValid || got.Address.PersonaID != "helper" || got.Address.Reason != wantReason || got.Address.Remainder != "" || got.Err != nil {
			t.Fatalf("ResolveExactControl(%q) = %+v, want helper exact control", text, got)
		}
	}
}

func TestRosterResolveExactControlCoordinatorPunctuatedAliasAmbiguityRefusal(t *testing.T) {
	r, err := NewRoster([]Identity{
		{ID: "santa", DisplayName: "Santa"},
		{ID: "helper-a", DisplayName: "Helper A", Aliases: []string{"Santa!"}},
		{ID: "helper-b", DisplayName: "Helper B", Aliases: []string{"Santa!"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"Santa!", "Hey Santa!", "switch to Santa!", "Santa!!", "Santa!?", "Santa!\u2026", "Hey Santa!!", "switch to Santa!!"} {
		got := r.ResolveExactControl(text)
		var collision *AmbiguousTargetError
		if got.Kind != ExactControlInvalid || !errors.As(got.Err, &collision) {
			t.Fatalf("ResolveExactControl(%q) = %+v, want exact invalid ambiguity", text, got)
		}
		if got.Address.PersonaID != "" || got.Address.Remainder != text || got.Address.Reason != ReasonAmbiguousTarget {
			t.Fatalf("ResolveExactControl(%q) address = %+v", text, got.Address)
		}
	}
}

func TestRosterResolveExactControlSupportsUnicodeFullNames(t *testing.T) {
	r, err := NewRoster([]Identity{{ID: "north", DisplayName: "Sánta North"}})
	if err != nil {
		t.Fatal(err)
	}
	got := r.ResolveExactControl("Sánta North.")
	if got.Kind != ExactControlValid || got.Address.PersonaID != "north" || got.Err != nil {
		t.Fatalf("unicode exact control = %+v", got)
	}
}
