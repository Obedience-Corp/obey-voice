package brain

import (
	"encoding/json"
	"net/http"
	"os"
	"testing"

	"github.com/Obedience-Corp/obey-voice/internal/testmedia"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/chatmedia"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/ollama/ollama/api"
)

func TestOllamaImagesReachModelAndSurviveHistory(t *testing.T) {
	config.SetConfigDirForTest(t, t.TempDir())
	r, err := chatmedia.DefaultStore().Import("animation.gif", testmedia.GIF())
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	o := repetitionBrain(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/show" {
			_ = json.NewEncoder(w).Encode(map[string]any{"capabilities": []string{"completion", "vision"}})
			return
		}
		var req api.ChatRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
		}
		found := false
		for _, m := range req.Messages {
			if len(m.Images) > 0 {
				found = true
				if string(m.Images[0][:8]) != "\x89PNG\r\n\x1a\n" {
					t.Error("GIF not converted to PNG first frame")
				}
			}
		}
		if !found {
			t.Error("request lost image bytes")
		}
		calls++
		_ = json.NewEncoder(w).Encode(api.ChatResponse{Model: "fixture", Message: api.Message{Role: "assistant", Content: "Two colored blocks."}, Done: true})
	})
	if _, err = o.ThinkFull(t.Context(), "Describe", StreamOptions{Attachments: []chatmedia.Ref{r}}); err != nil {
		t.Fatal(err)
	}
	history := o.History()
	last := len(history) - 2
	if len(history[last].Attachments) != 1 {
		t.Fatal("canonical history lost media")
	}
	history[last].Attachments[0].Name = "changed"
	if o.History()[last].Attachments[0].Name == "changed" {
		t.Fatal("history aliases media references")
	}
	if err = o.ImportHistory(o.History()); err != nil {
		t.Fatal(err)
	}
	if _, err = o.ThinkFull(t.Context(), "What color?", StreamOptions{}); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("calls=%d", calls)
	}
	if err = os.Remove(chatmedia.DefaultStore().Path(r)); err != nil {
		t.Fatal(err)
	}
	if _, err = o.ThinkFull(t.Context(), "Again", StreamOptions{}); err == nil {
		t.Fatal("missing asset was silently omitted")
	}
}
func TestOllamaRejectsTextOnlyModelBeforeHistoryChanges(t *testing.T) {
	o := repetitionBrain(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/show" {
			t.Error("incompatible input reached model")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"capabilities": []string{"completion"}})
	})
	n := len(o.History())
	_, err := o.ThinkFull(t.Context(), "look", StreamOptions{Attachments: []chatmedia.Ref{{ID: "image"}}})
	if err == nil || len(o.History()) != n {
		t.Fatal("rejected image mutated history")
	}
}
