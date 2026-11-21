package summon

import (
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/textclean"
)

func TestAgentMentionControls(t *testing.T) {
	r, err := NewRoster([]Identity{
		{ID: "venessa-agent", DisplayName: "Venessa", Aliases: []string{"V", "shared"}},
		{ID: "obey", DisplayName: "Obey", Aliases: []string{"shared"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		text, id string
		kind     ExactControlKind
	}{
		{"Sure thing, Lance.\n@Venessa Hey Venessa!", "venessa-agent", ExactControlValid},
		{"@VENESSA-AGENT: Hello!", "venessa-agent", ExactControlValid},
		{"Your thoughts, @V?", "venessa-agent", ExactControlValid},
		{"@Venessa hello. @V anything else?", "venessa-agent", ExactControlValid},
		{"@missing hello", "", ExactControlInvalid},
		{"@shared hello", "", ExactControlInvalid},
		{"@Venessa and @Obey hello", "", ExactControlInvalid},
		{"Venessa likes festivals.", "", ExactControlNone},
		{"venessa@example.com", "", ExactControlNone},
		{"Say \"@Venessa hello\".", "", ExactControlNone},
	} {
		t.Run(tc.text, func(t *testing.T) {
			got := r.ResolveAgentMention(tc.text)
			if got.Kind != tc.kind || got.Address.PersonaID != tc.id {
				t.Fatalf("got %+v", got)
			}
		})
	}
}

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

func TestLeadingPathMentionIsNotAnIdentityControl(t *testing.T) {
	r, err := NewRoster([]Identity{{ID: "joker", DisplayName: "The Joker"}, {ID: "projects", DisplayName: "Projects"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		input string
		kind  ExactControlKind
	}{
		{"@projects/obey-voice/ what is this", ExactControlNone},
		{"@projects/joker/ hi", ExactControlNone},
		{"@joker/ hi", ExactControlNone},
		{"@./README.md summarize this", ExactControlNone},
		{"@workflow/design/", ExactControlNone},
		{"@/home/me/camp/projects/joker/ what is this", ExactControlNone},
		{"@missing help", ExactControlInvalid},
		{"@ help", ExactControlInvalid},
		{"@joker help", ExactControlValid},
		{"@projects hi", ExactControlValid},
	} {
		t.Run(tc.input, func(t *testing.T) {
			if got := r.ResolveExactControl(tc.input); got.Kind != tc.kind {
				t.Fatalf("ResolveExactControl = %+v, want kind %d", got, tc.kind)
			}
			got := r.ResolveForegroundControl(tc.input)
			if got.Kind != tc.kind {
				t.Fatalf("ResolveForegroundControl = %+v, want kind %d", got, tc.kind)
			}
		})
	}
}

func TestPathMentionsAddressNobody(t *testing.T) {
	r, err := NewRoster([]Identity{{ID: "joker", DisplayName: "Joker"}, {ID: "projects", DisplayName: "Projects"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{
		"Look at @projects/joker/ for me.",
		"Open @./README.md please",
		"See @joker/notes.md and @projects/",
	} {
		if mentions := textclean.AgentMentions(text); len(mentions) != 0 {
			t.Fatalf("%q: AgentMentions = %+v", text, mentions)
		}
		if got := r.ResolveAgentMention(text); got.Kind != ExactControlNone {
			t.Fatalf("%q: ResolveAgentMention = %+v", text, got)
		}
	}
}
