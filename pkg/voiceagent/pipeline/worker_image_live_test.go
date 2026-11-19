//go:build integration

package pipeline

import (
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/internal/testmedia"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/chatmedia"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

// The worker must obtain an image from a server, write it in its empty work
// directory and attach it. Neither the user's files nor a public site are needed.
func TestLiveWorkerFetchesRequestedPictureAfterRefusal(t *testing.T) {
	model := os.Getenv("OBEY_VOICE_IMAGE_ROUTING_MODEL")
	if model == "" {
		t.Skip("set OBEY_VOICE_IMAGE_ROUTING_MODEL to an installed Ollama model")
	}
	config.SetConfigDirForTest(t, t.TempDir())
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(testmedia.PNG())
	}))
	defer source.Close()
	p, b, _ := harnessPipeline(t, false)
	cfg := p.Agency.currentConfig()
	cfg.OllamaModel = model
	cfg.BrainProvider = "ollama"
	cfg.PersonaWork = config.WorkOwn
	cfg.OllamaHost = "http://localhost:11434"
	cfg.OllamaNumCtx = 32768
	cfg.AgentName = "Obey"
	cfg.MaxHistory = 20
	p.Agency.ReplaceWorker(nil, cfg, nil)
	b.turns = []brain.Turn{{Role: "user", Content: "Show me a picture of a monkey."}, {Role: "assistant", Content: "I can't fetch or display pictures directly in this chat."}}
	done := make(chan events.JobFinished, 1)
	fail := make(chan events.JobFailed, 1)
	events.Subscribe(p.Events, func(e events.JobFinished) { done <- e })
	events.Subscribe(p.Events, func(e events.JobFailed) { fail <- e })
	if err := p.RunTurnTextMode(t.Context(), "Can you show me a picture of a monkey? Use this supplied image URL: "+source.URL+"/monkey.png"); err != nil {
		t.Fatal(err)
	}
	select {
	case e := <-done:
		if len(e.Attachments) != 1 {
			t.Fatalf("missing image: %+v", e)
		}
		ref := e.Attachments[0]
		if _, err := chatmedia.DefaultStore().Preview(ref); err != nil {
			t.Fatal(err)
		}
		want, err := chatmedia.DefaultStore().Import("fixture.png", testmedia.PNG())
		if err != nil || ref.ID != want.ID {
			t.Fatalf("returned a different image: %+v, %v", ref, err)
		}
	case e := <-fail:
		t.Fatal(e.Err)
	case <-time.After(3 * time.Minute):
		t.Fatal("worker did not deliver image")
	}
}
