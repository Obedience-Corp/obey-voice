package prompts

import (
	"strings"
	"testing"
)

// Prompt documents written before the obey-voice rename declare the samantha
// schema tag and must keep loading unchanged.
func TestLoadAcceptsLegacySchema(t *testing.T) {
	legacy := strings.Replace(validDoc, Schema, LegacySchema, 1)
	doc, err := Load([]byte(legacy))
	if err != nil {
		t.Fatalf("Load(legacy schema) error = %v", err)
	}
	if doc.Schema != LegacySchema {
		t.Fatalf("doc.Schema = %q, want %q preserved", doc.Schema, LegacySchema)
	}
	if _, err := Load([]byte(strings.Replace(validDoc, Schema, "samantha.prompt.v2", 1))); err == nil {
		t.Fatal("Load(unknown schema) error = nil, want rejection")
	}
}
