package pipeline

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/session"
	"github.com/ollama/ollama/api"
)

// Routing models can put untagged planning in content before their tool call.
// Drive those bytes through synthesis and persistence, including an answer
// without a sentence terminator so the final speech flush is exercised.
func TestRoutingPlanningCannotReplaceSpokenReply(t *testing.T) {
	const planning = "The user is addressing me directly. I need to respond in character."
	const reply = "Good evening. How can I help"
	for _, route := range []bool{true, false} {
		t.Run(map[bool]string{true: "route then answer", false: "unrouted answer retries before speech"}[route], func(t *testing.T) {
			requests := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/tags" {
					_, _ = w.Write([]byte(`{"models":[{"name":"fixture"}]}`))
					return
				}
				var req api.ChatRequest
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Error(err)
					return
				}
				requests++
				if requests == 1 && len(req.Tools) == 0 {
					t.Error("first request lost routing tools")
				}
				if (route && requests > 1) || (!route && requests > 2) {
					if len(req.Tools) != 0 {
						t.Error("chat answer offered routing tools again")
					}
					for _, msg := range req.Messages {
						if strings.Contains(msg.Content, planning) {
							t.Error("planning leaked into model history")
						}
					}
				}
				enc := json.NewEncoder(w)
				if requests == 2 && !route {
					if len(req.Format) == 0 || len(req.Tools) != 0 {
						t.Error("missing-route retry must use constrained JSON")
					}
					_ = enc.Encode(api.ChatResponse{Message: api.Message{Role: "assistant", Content: `{"kind":"chat"}`}, Done: true})
					return
				}
				if requests == 1 && route {
					for _, part := range []string{planning[:32], planning[32:]} {
						_ = enc.Encode(api.ChatResponse{Message: api.Message{Role: "assistant", Content: part}})
					}
					_, _ = w.Write([]byte(`{"message":{"role":"assistant","tool_calls":[{"function":{"name":"route","arguments":{"kind":"chat"}}}]},"done":true}` + "\n"))
					return
				}
				for _, part := range []string{"Good evening. ", "How can I help"} {
					_ = enc.Encode(api.ChatResponse{Message: api.Message{Role: "assistant", Content: part}})
				}
				_ = enc.Encode(api.ChatResponse{Done: true})
			}))
			defer srv.Close()
			root := t.TempDir()
			b, err := brain.NewOllama(&config.Config{OllamaHost: srv.URL, OllamaModel: "fixture", AgentName: "Guide", MaxHistory: 20, PromptsDir: filepath.Join(root, "prompts"), WorkDir: root})
			if err != nil {
				t.Fatal(err)
			}
			tts := &progressiveFakeTTS{}
			player := newFakePlayer(time.Millisecond)
			defer player.Close()
			p := &Pipeline{TTS: tts, Player: player, Events: events.NewBus()}
			var visible, thinking strings.Builder
			events.Subscribe(p.Events, func(e events.ResponseDelta) { visible.WriteString(e.Text) })
			events.Subscribe(p.Events, func(e events.ThinkingDelta) { thinking.WriteString(e.Text) })
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			stream, err := b.ThinkStream(ctx, "Good evening", brain.StreamOptions{ToolsEnabled: true, RouterToolsPolicy: brain.RouterToolsRouteOnly})
			if err != nil {
				t.Fatal(err)
			}
			metrics := newTurnMetrics()
			transcript, _, err := p.streamResponse(ctx, cancel, stream, false, metrics, p.newTurnConductor(metrics), nil)
			if err != nil {
				t.Fatal(err)
			}
			for dest, got := range map[string]string{"speech": strings.Join(tts.Calls(), " "), "transcript": transcript, "response deltas": visible.String()} {
				if strings.TrimSpace(got) != reply {
					t.Errorf("%s = %q, want %q", dest, got, reply)
				}
			}
			if route && thinking.String() != planning {
				t.Errorf("thinking = %q", thinking.String())
			}
			wantRequests := 3
			if route {
				wantRequests = 2
			}
			if requests != wantRequests {
				t.Errorf("requests = %d, want %d", requests, wantRequests)
			}
			store := session.NewStore(filepath.Join(root, "sessions"))
			sess := session.New("ollama", "fixture")
			if err := store.Save(sess, b.History()); err != nil {
				t.Fatal(err)
			}
			saved, err := store.Load(sess.ID)
			if err != nil {
				t.Fatal(err)
			}
			for _, turn := range saved.Turns {
				if strings.Contains(turn.Content, planning) {
					t.Error("persisted planning as reply")
				}
			}
			if last := saved.Turns[len(saved.Turns)-1]; last.Content != reply {
				t.Errorf("persisted answer = %q", last.Content)
			}
		})
	}
}
