package correct

import "testing"

func TestKey(t *testing.T) {
	tests := []struct{ in, want string }{
		{"Sam antha", "samantha"}, {"obey-voice", "obeyvoice"}, {"SAMANTHA!", "samantha"},
		{"Zoë", "zo"}, {"...", ""}, {"Qwen3.5", "qwen35"},
	}
	for _, tc := range tests {
		if got := Key(tc.in); got != tc.want {
			t.Errorf("Key(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestLevenshtein(t *testing.T) {
	tests := []struct {
		a, b string
		want int
	}{
		{"", "", 0}, {"a", "", 1}, {"", "abc", 3}, {"kitten", "sitting", 3},
		{"veronika", "veronica", 1}, {"olama", "ollama", 1}, {"worked", "workitem", 3},
	}
	for _, tc := range tests {
		if got := Levenshtein(tc.a, tc.b); got != tc.want {
			t.Errorf("Levenshtein(%q, %q) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestSoundex(t *testing.T) {
	tests := []struct{ in, want string }{
		{"robert", "R163"}, {"rupert", "R163"}, {"rubin", "R150"}, {"ashcraft", "A261"},
		{"tymczak", "T522"}, {"pfister", "P236"}, {"honeyman", "H555"}, {"", ""}, {"123", ""},
		{"veronika", "V652"}, {"veronica", "V652"},
		{"ollama", "A450"}, {"alama", "A450"}, {"elena", "A450"}, {"ivan", "A150"},
	}
	for _, tc := range tests {
		if got := Soundex(tc.in); got != tc.want {
			t.Errorf("Soundex(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestIsCommon(t *testing.T) {
	for _, w := range []string{"worked", "cloud", "nice", "sent", "santa", "joker", "festival", "the"} {
		if !IsCommon(w) {
			t.Errorf("IsCommon(%q) = false", w)
		}
	}
	for _, w := range []string{"samantha", "veronica", "jarvis", "ollama", "qwen", "workitem", ""} {
		if IsCommon(w) {
			t.Errorf("IsCommon(%q) = true", w)
		}
	}
}
