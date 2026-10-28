package summon

import "testing"

func TestExplicitPersonaMentions(t *testing.T) {
	r, err := NewRoster([]Identity{{ID: "joker", DisplayName: "The Joker"}, {ID: "other", DisplayName: "Joker"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		input, id, body string
		kind            ExactControlKind
		bare            bool
	}{
		{"@joker what is next?", "joker", "what is next?", ExactControlValid, false},
		{"@JOKER\nPlease explain.", "joker", "Please explain.", ExactControlValid, false},
		{"@joker", "joker", "", ExactControlValid, true},
		{"@missing help", "", "", ExactControlInvalid, false},
		{"@ help", "", "", ExactControlInvalid, false},
		{"email joker@example.com", "", "", ExactControlNone, false},
		{"Tell @joker about it", "", "", ExactControlNone, false},
		{"\"@joker help\"", "", "", ExactControlNone, false},
	} {
		t.Run(tc.input, func(t *testing.T) {
			got := r.ResolveExactControl(tc.input)
			if got.Kind != tc.kind || got.Address.PersonaID != tc.id || (tc.kind == ExactControlValid && (got.Address.Remainder != tc.body || got.Address.Bare != tc.bare)) {
				t.Fatalf("got %+v", got)
			}
		})
	}
}
