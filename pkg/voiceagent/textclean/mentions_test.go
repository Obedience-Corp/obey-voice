package textclean

import "testing"

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
	} {
		t.Run(tc.input, func(t *testing.T) {
			if got := StripAgentMentions(tc.input); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}
