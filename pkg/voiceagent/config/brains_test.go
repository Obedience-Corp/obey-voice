package config

import (
	"strings"
	"testing"
)

func TestJoinEnglish(t *testing.T) {
	tests := []struct {
		items []string
		conj  string
		want  string
	}{
		{nil, "and", ""},
		{[]string{"claude"}, "or", "claude"},
		{[]string{"claude", "grok"}, "or", "claude or grok"},
		{[]string{"claude", "cursor", "grok"}, "and", "claude, cursor, and grok"},
		{[]string{"claude", "cursor", "grok", "ollama"}, "or", "claude, cursor, grok, or ollama"},
	}
	for _, tt := range tests {
		if got := joinEnglish(tt.items, tt.conj); got != tt.want {
			t.Errorf("joinEnglish(%q, %q) = %q, want %q", tt.items, tt.conj, got, tt.want)
		}
	}
}

func TestDiagnoseUnsupportedListsRegisteredProviders(t *testing.T) {
	got := diagnoseBrainProvider(&Config{BrainProvider: "other"}, okLookPath)
	for _, name := range brainProviderEnum() {
		if !strings.Contains(got.Remediation, name) {
			t.Errorf("remediation %q missing registered provider %q", got.Remediation, name)
		}
	}
}
