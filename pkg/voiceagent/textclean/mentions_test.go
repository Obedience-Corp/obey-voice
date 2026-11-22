package textclean

import (
	"strings"
	"testing"
)

func TestSpeakAgentMentions(t *testing.T) {
	names := map[string]string{"venessa": "Venessa", "vee": "Venessa", "milo": "Milo", "agent-two": "Agent Two", "élodie": "Élodie", "agent_one": "Agent One"}
	known := func(target string) (string, bool) {
		name, ok := names[strings.ToLower(target)]
		return name, ok
	}
	for _, tc := range []struct{ input, want string }{
		{"@Milo, what do you think?", "Milo, what do you think?"},
		{"@Milo @Venessa, what do you two think?", "Milo, Venessa, what do you two think?"},
		{"@Milo and @Venessa, what do you two think?", "Milo and Venessa, what do you two think?"},
		{"@Milo, @Venessa: thoughts?", "Milo, Venessa: thoughts?"},
		{"Sure thing.\n@Venessa: What do you think?", "Sure thing. Venessa: What do you think?"},
		{"Your thoughts, @agent-two?", "Your thoughts, Agent Two?"},
		{"@Vee, go ahead.", "Venessa, go ahead."},
		{"@Élodie, bonjour!", "Élodie, bonjour!"},
		{"@agent_one", "Agent One"},
		{"@Venessa Hey Venessa!", "Hey Venessa!"},
		{"Venessa, your turn. @Venessa", "Venessa, your turn."},
		{"@Vee @Venessa @Milo, thoughts?", "Venessa, Milo, thoughts?"},
		{"@Milo @Vee @Venessa, thoughts?", "Milo, Venessa, thoughts?"},
		{"@Milo @Venessa, Milo first: thoughts?", "Venessa, Milo first: thoughts?"},
		{"Venessa, what do you think?", "Venessa, what do you think?"},
		{"Email venessa@example.com or visit https://example.com/@venessa.", "Email venessa@example.com or visit https://example.com/@venessa."},
		{"Use `@Venessa` or say \"@Venessa hello\".", "Use `@Venessa` or say \"@Venessa hello\"."},
		{"@example.com is a domain.", "@example.com is a domain."},
		{"Contact @support for help with @typescript.", "Contact @support for help with @typescript."},
		{"@support", "@support"},
		{"@Milo Contact @support for help.", "Milo Contact @support for help."},
		{"Contact @support.\n\nThen ask @someone_else.", "Contact @support.\n\nThen ask @someone_else."},
	} {
		t.Run(tc.input, func(t *testing.T) {
			if got := SpeakAgentMentions(tc.input, known); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
			if got := SpeakAgentMentions(tc.input, nil); got != tc.input {
				t.Fatalf("without a roster lookup, got %q, want original %q", got, tc.input)
			}
		})
	}
}
