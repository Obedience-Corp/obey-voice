package stt

import "testing"

func TestGhostTranscriptMatchesIsolatedYeah(t *testing.T) {
	t.Parallel()
	ghosts := []string{"Yeah.", "yeah", "YUP!", "You.", "mm-hmm", "Thanks for watching."}
	for _, in := range ghosts {
		if !isGhostTranscript(in) {
			t.Errorf("isGhostTranscript(%q) = false, want true", in)
		}
	}
	keep := []string{"yeah start the app", "Yes.", "okay", "start the app"}
	for _, in := range keep {
		if isGhostTranscript(in) {
			t.Errorf("isGhostTranscript(%q) = true, want false", in)
		}
	}
}

func TestNormalizeTranscriptDropsNonSpeechTags(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"blank audio", "[BLANK_AUDIO]", ""},
		{"music", "[MUSIC]", ""},
		{"bell", "[Bell]", ""},
		{"parens applause", "(applause)", ""},
		{"asterisk buzzer", "*buzzer*", ""},
		{"spaced tag", "[ Music ]", ""},
		{"multiple tags", "[MUSIC] [Bell]", ""},
		{"tag around speech", "[Music] git status", "git status"},
		{"speech then tag", "open the door (creak)", "open the door"},
		{"plain speech untouched", "what time is it", "what time is it"},
		// Truncated sound-event tags (streaming partial / unclosed final).
		// These used to survive normalize and start spurious turns — e.g. "[Buzz".
		{"incomplete buzz bracket", "[Buzz", ""},
		{"incomplete blank audio", "[BLANK_AUDIO", ""},
		{"incomplete parens", "(creak", ""},
		{"speech then incomplete buzz", "open the door [Buzz", "open the door"},
		{"complete buzz still drops", "[Buzz]", ""},
		// Asterisk tags still need a close; do not strip math-like speech.
		{"asterisk speech untouched", "multiply * 2 please", "multiply * 2 please"},
		// Isolated STT ghosts (echo / clipped VAD / non-speech decode).
		{"yeah ghost", "Yeah.", ""},
		{"yeah lowercase", "yeah", ""},
		{"you ghost", "You.", ""},
		{"thanks for watching", "Thanks for watching!", ""},
		{"real yeah stays", "yeah start the app", "yeah start the app"},
		{"real sentence stays", "Can you start the app for me?", "Can you start the app for me?"},
		{"yes is a real reply", "Yes.", "Yes."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := normalizeTranscript(tt.in); got != tt.want {
				t.Fatalf("normalizeTranscript(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
