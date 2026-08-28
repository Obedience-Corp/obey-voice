package qwen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUseManaged(t *testing.T) {
	tests := []struct {
		name, binary, model string
		want                bool
	}{
		{"empty", "", "", true},
		{"legacy cli default", "qwen3-tts-cli", "", true},
		{"explicit model", "qwen3-tts-cli", "/m", false},
		{"explicit worker", "/opt/w", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := UseManaged(tt.binary, tt.model); got != tt.want {
				t.Fatalf("UseManaged(%q,%q)=%v want %v", tt.binary, tt.model, got, tt.want)
			}
		})
	}
}

func TestInspectEmpty(t *testing.T) {
	dir := t.TempDir()
	st := Inspect(dir)
	if st.Installed {
		t.Fatalf("%+v", st)
	}
	if !strings.Contains(st.Detail, "not installed") {
		t.Fatalf("detail = %q, want not installed", st.Detail)
	}
	if ResolveNativeURL("") == "" && !strings.Contains(st.Detail, "no published package") {
		t.Fatalf("unpublished platform detail = %q, want no published package", st.Detail)
	}
	if ResolveNativeURL("") == "" && strings.Contains(st.Detail, "uses published platform release") {
		t.Fatalf("must not promise a download that does not exist: %q", st.Detail)
	}
}

func TestCanonicalVoice(t *testing.T) {
	if v, ok := CanonicalVoice("vivian"); !ok || v != "Vivian" {
		t.Fatalf("%v %v", v, ok)
	}
}

func TestDefaultVoiceIsShippedVeronica(t *testing.T) {
	if DefaultVoice != "Veronica" {
		t.Fatalf("DefaultVoice = %q, want Veronica", DefaultVoice)
	}
	if _, ok := LookupShipped(DefaultVoice); !ok {
		t.Fatal("DefaultVoice must resolve as a shipped clone")
	}
}

func TestInspectIgnoresStrayPythonWorkerFile(t *testing.T) {
	// Solo installs: leftover files are not a product state machine.
	dir := t.TempDir()
	root := filepath.Join(dir, ProviderName, "worker")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "qwen_worker.py"), []byte("# junk\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	st := Inspect(dir)
	if st.Installed {
		t.Fatalf("stray python file must not count as installed: %+v", st)
	}
}
