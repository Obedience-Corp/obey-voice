package config

import (
	"os"
	"strings"
	"testing"
)

func TestConversationMemoryPreservesConfigAndSetsBothLimits(t *testing.T) {
	SetConfigDirForTest(t, t.TempDir())
	source := "# personal settings\nollama_num_ctx: 8192 # small machine\ncontinuity_num_ctx_ceil: 8192\ntts_provider: kokoro\n"
	if err := os.WriteFile(ConfigFile(), []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	for _, n := range []int{16384, 32768, 8192} {
		if err := SetConversationMemory(n); err != nil {
			t.Fatal(err)
		}
		cfg, err := LoadWithoutHooks()
		if err != nil {
			t.Fatal(err)
		}
		if cfg.OllamaNumCtx != n || cfg.ContinuityNumCtxCeil != n || cfg.VoiceWorkerNumCtx != 8192 {
			t.Fatalf("limits=%d/%d worker=%d", cfg.OllamaNumCtx, cfg.ContinuityNumCtxCeil, cfg.VoiceWorkerNumCtx)
		}
	}
	data, err := os.ReadFile(ConfigFile())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "# small machine") || !strings.Contains(string(data), "tts_provider: kokoro") {
		t.Fatalf("unrelated config changed:\n%s", data)
	}
	if err := SetConversationMemory(262144); err == nil {
		t.Fatal("invalid preset accepted")
	}
	after, _ := os.ReadFile(ConfigFile())
	if string(after) != string(data) {
		t.Fatal("invalid preset changed config")
	}
	malformed := "ollama_num_ctx: 8192\ncontinuity_num_ctx_ceil: [\n"
	if err := os.WriteFile(ConfigFile(), []byte(malformed), 0600); err != nil {
		t.Fatal(err)
	}
	if err := SetConversationMemory(32768); err == nil {
		t.Fatal("malformed config accepted")
	}
	after, _ = os.ReadFile(ConfigFile())
	if string(after) != malformed {
		t.Fatal("failed preset partially rewrote config")
	}
}
