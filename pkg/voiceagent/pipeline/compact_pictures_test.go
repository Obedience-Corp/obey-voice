package pipeline

import (
	"bytes"
	"encoding/json"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/chatmedia"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/ollama/ollama/api"
)

// A picture posted before the message still in the image window rides on it
// after the conversation is compacted.
func TestCompactionKeepsAPostedPictureOnItsMessage(t *testing.T) {
	config.SetConfigDirForTest(t, t.TempDir())
	var mu sync.Mutex
	var requests []api.ChatRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/api/tags"):
			_, _ = io.WriteString(w, `{"models":[{"name":"m"}]}`)
		case strings.HasSuffix(r.URL.Path, "/api/show"):
			_, _ = io.WriteString(w, `{"capabilities":["completion","vision"]}`)
		default:
			var req api.ChatRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Error(err)
				return
			}
			mu.Lock()
			requests = append(requests, req)
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(api.ChatResponse{Model: "m", Message: api.Message{Role: "assistant", Content: "It is grey."}, Done: true, DoneReason: "stop"})
		}
	}))
	t.Cleanup(srv.Close)
	b, err := brain.NewOllama(&config.Config{OllamaHost: srv.URL, OllamaModel: "m", MaxHistory: 20, PromptsDir: t.TempDir(), WorkDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = brain.CloseProvider(b) })
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewGray(image.Rect(0, 0, 32, 32))); err != nil {
		t.Fatal(err)
	}
	chart, err := chatmedia.DefaultStore().Import("chart.png", buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.ThinkFull(t.Context(), "Draw me a chart.", brain.StreamOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := b.SetReplyMedia("Here is the chart.\nchart.png", []chatmedia.Ref{chart}); err != nil {
		t.Fatal(err)
	}
	if _, err := b.ThinkFull(t.Context(), "What colour is it?", brain.StreamOptions{}); err != nil {
		t.Fatal(err)
	}
	p := &Pipeline{Brain: b, CompactPrompt: "Summarize the conversation."}
	if err := p.CompactConversation(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := b.ThinkFull(t.Context(), "And now?", brain.StreamOptions{}); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	last := requests[len(requests)-1]
	mu.Unlock()
	if strings.Contains(last.Messages[1].Content, "Draw me a chart.") {
		t.Fatal("the conversation was not compacted")
	}
	line := "Picture " + chart.ID + " attached: chart.png, a picture you posted."
	for _, m := range last.Messages {
		if strings.Contains(m.Content, "What colour is it?") {
			if len(m.Images) != 1 || strings.Count(m.Content, line) != 1 {
				t.Fatalf("after compaction the question carries %d images: %q", len(m.Images), m.Content)
			}
			return
		}
	}
	t.Fatal("the question is not in the request")
}
