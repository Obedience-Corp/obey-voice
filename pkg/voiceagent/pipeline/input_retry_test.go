package pipeline

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/session"
)

func TestInputRetryReusesPersistedUserTurn(t *testing.T) {
	for _, mode := range []string{"recover", "partial", "clear", "new input", "rebound", "restore"} {
		t.Run(mode, func(t *testing.T) {
			var healthy atomic.Bool
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/tags" {
					_, _ = fmt.Fprint(w, `{"models":[{"name":"fixture"}]}`)
					return
				}
				calls.Add(1)
				if !healthy.Load() {
					if mode == "partial" {
						_, _ = fmt.Fprintln(w, `{"message":{"role":"assistant","content":"Partial answer."}}`)
					} else {
						w.WriteHeader(500)
					}
					_, _ = fmt.Fprintln(w, `{"error":"mlx runner exited unexpectedly: signal: broken pipe"}`)
					return
				}
				var req struct {
					Messages []struct{ Role, Content string }
				}
				_ = json.NewDecoder(r.Body).Decode(&req)
				users := 0
				for _, message := range req.Messages {
					if message.Role == "user" {
						users++
					}
				}
				if mode == "recover" && users != 1 {
					t.Errorf("wire user entries=%d", users)
				}
				_, _ = fmt.Fprintln(w, `{"message":{"role":"assistant","content":"Recovered answer."},"done":true,"done_reason":"stop"}`)
			}))
			defer server.Close()
			cfg := &config.Config{OllamaHost: server.URL, OllamaModel: "fixture", OllamaNumCtx: 8192, MaxHistory: 40, PromptsDir: t.TempDir(), WorkDir: t.TempDir()}
			b, err := brain.NewOllama(cfg)
			if err != nil {
				t.Fatal(err)
			}
			bus := events.NewBus()
			var inputs atomic.Int32
			events.Subscribe(bus, func(events.UserInput) { inputs.Add(1) })
			p := &Pipeline{Brain: b, Events: bus}
			if err := p.RunTurnTextMode(t.Context(), "Remember cobalt."); err != nil {
				t.Fatal(err)
			}
			healthy.Store(true)
			if mode == "rebound" {
				p.ReplaceTTS(nil, nil)
			}
			if mode == "restore" {
				b.LoadHistory(b.History())
			}
			if mode == "clear" {
				b.ClearHistory()
			}
			if mode == "new input" {
				if err := p.RunTurnTextMode(t.Context(), "A newer request."); err != nil {
					t.Fatal(err)
				}
			}
			err = p.RetryLastTurn(t.Context())
			if mode != "recover" {
				if err == nil {
					t.Fatal("unsafe or expired retry dispatched")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if calls.Load() != 3 || inputs.Load() != 1 {
				t.Fatalf("requests=%d user events=%d", calls.Load(), inputs.Load())
			}
			store := session.NewStore(t.TempDir())
			saved := session.New("ollama", "fixture")
			if err := store.Save(saved, b.History()); err != nil {
				t.Fatal(err)
			}
			reloaded, err := store.Load(saved.ID)
			if err != nil {
				t.Fatal(err)
			}
			if len(reloaded.Turns) != 2 || reloaded.Turns[0].Role != "user" || reloaded.Turns[0].Content != "Remember cobalt." || reloaded.Turns[1].Content != "Recovered answer." {
				t.Fatalf("persisted turns=%+v", reloaded.Turns)
			}
			if err := p.RetryLastTurn(t.Context()); err == nil {
				t.Fatal("completed input remained retryable")
			}
		})
	}
}
