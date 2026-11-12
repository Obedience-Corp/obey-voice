package prompts

import (
	"os"
	"path/filepath"
	"testing"
)

func BenchmarkRenderSystemEmbeddedNamed(b *testing.B) {
	dir := b.TempDir()
	values := map[string]string{"agent_name": "Ada"}
	b.ReportAllocs()
	for b.Loop() {
		if _, err := RenderSystem(dir, "current-request", values); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkRenderSystemUserFile(b *testing.B) {
	dir := b.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "system"), 0o700); err != nil {
		b.Fatal(err)
	}
	body := "schema: obey-voice.prompt.v1\nprompt:\n  name: current-request\n  kind: system\n  system_prompt: 'Reply as {agent_name}.'\n"
	if err := os.WriteFile(filepath.Join(dir, "system", "current-request.yaml"), []byte(body), 0o600); err != nil {
		b.Fatal(err)
	}
	values := map[string]string{"agent_name": "Ada"}
	b.ReportAllocs()
	for b.Loop() {
		if _, err := RenderSystem(dir, "current-request", values); err != nil {
			b.Fatal(err)
		}
	}
}
