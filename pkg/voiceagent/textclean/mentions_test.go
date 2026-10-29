package textclean

import (
	"strings"
	"testing"
)

func TestStripAgentMentions(t *testing.T) {
	for _, tc := range []struct{ input, want string }{
		{"@Venessa Hey Venessa!", "Hey Venessa!"},
		{"Sure thing.\n@Venessa: What do you think?", "Sure thing. What do you think?"},
		{"Your thoughts, @agent-two?", "Your thoughts?"},
		{"@Élodie, bonjour!", "bonjour!"},
		{"@agent_one", ""},
		{"Venessa, what do you think?", "Venessa, what do you think?"},
		{"Email venessa@example.com or visit https://example.com/@venessa.", "Email venessa@example.com or visit https://example.com/@venessa."},
		{"Use `@Venessa` or say \"@Venessa hello\".", "Use `@Venessa` or say \"@Venessa hello\"."},
		{"@example.com is a domain.", "@example.com is a domain."},
		{"Contact @support for help with @typescript.", "Contact @support for help with @typescript."},
		{"@support", "@support"},
		{"@Venessa Contact @support for help.", "Contact @support for help."},
		{"Contact @support.\n\nThen ask @someone_else.", "Contact @support.\n\nThen ask @someone_else."},
	} {
		t.Run(tc.input, func(t *testing.T) {
			known := func(target string) bool {
				return strings.EqualFold(target, "Venessa") || target == "agent-two" || target == "Élodie" || target == "agent_one"
			}
			if got := StripAgentMentions(tc.input, known); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
			if got := StripAgentMentions(tc.input, nil); got != tc.input {
				t.Fatalf("without a roster lookup, got %q, want original %q", got, tc.input)
			}
		})
	}
}
