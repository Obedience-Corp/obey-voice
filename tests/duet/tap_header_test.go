package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestTapHeaderReady(t *testing.T) {
	for _, tc := range []struct {
		name, line string
		ready      bool
	}{
		{"legacy", `{"type":"header","v":1}`, true},
		{"current", `{"type":"header","v":2,"trace_schema":"obey-voice.trace/v1","future":true}`, true},
		{"unsupported", `{"type":"header","v":3}`, false},
		{"not header", `{"type":"user","v":2}`, false},
		{"malformed", `{`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "events.jsonl")
			if err := os.WriteFile(path, []byte(tc.line+"\n"), 0600); err != nil {
				t.Fatal(err)
			}
			if got := tapHeaderReady(path); got != tc.ready {
				t.Fatalf("ready = %v, want %v", got, tc.ready)
			}
			if err := os.WriteFile(path, []byte(tc.line), 0600); err != nil {
				t.Fatal(err)
			}
			if tapHeaderReady(path) {
				t.Fatal("partial header reported ready")
			}
		})
	}
}
