//go:build integration

package brain

import (
	"net/http"
	"net/url"
	"os"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/ollama/ollama/api"
)

// Opt-in neutral model checks; regular CI uses deterministic wire fixtures.
func TestLiveRepetitionIntent(t *testing.T) {
	model := os.Getenv("OBEY_VOICE_LIVE_REPETITION_MODEL")
	if model == "" {
		t.Skip("set OBEY_VOICE_LIVE_REPETITION_MODEL to an installed Ollama model")
	}
	host := os.Getenv("OLLAMA_HOST")
	if host == "" {
		host = "http://localhost:11434"
	}
	base, err := url.Parse(host)
	if err != nil {
		t.Fatal(err)
	}
	o := &OllamaBrain{client: api.NewClient(base, http.DefaultClient), model: model, cfg: &config.Config{OllamaNumCtx: 32768, OllamaNumBatch: 128}}
	for _, tc := range []struct{ latest, want string }{
		{"Jarvis, you better respond to her.", "unrequested"},
		{"How much would the bench cost?", "unrequested"},
		{"Veronica, what do you think about the path?", "unrequested"},
		{"Please say that again.", "uncertain"},
		{"Repeat the garden plan and add a budget.", "uncertain"},
		{"Quote your last answer exactly.", "uncertain"},
		{"Could you run that by me once more?", "uncertain"},
		{"Yes.", "uncertain"},
		{"Okay.", "uncertain"},
	} {
		t.Run(tc.latest, func(t *testing.T) {
			got, err := o.repetitionIntent(t.Context(), tc.latest, StreamOptions{})
			if err != nil || got != tc.want {
				t.Fatalf("intent=%q err=%v want=%q", got, err, tc.want)
			}
		})
	}
}
