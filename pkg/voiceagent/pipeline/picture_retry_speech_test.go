package pipeline

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/ollama/ollama/api"
)

// A live picture request whose model answers in plain text, then makes a
// choice on the retry, through the real brain and the speech path.
func pictureRetryTurn(t *testing.T, choice string) (spoken []string, response string, errs []string, degraded bool, reply events.ResponseReady) {
	t.Helper()
	root := t.TempDir()
	config.SetConfigDirForTest(t, filepath.Join(root, "config"))
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, image.NewGray(image.Rect(0, 0, 24, 16)), nil); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "lion.jpg"), buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tags":
			_ = json.NewEncoder(w).Encode(map[string]any{"models": []map[string]string{{"name": "live"}}})
		case "/api/show":
			_ = json.NewEncoder(w).Encode(map[string]any{"capabilities": []string{"completion", "tools", "vision"}})
		default:
			msg := api.Message{Role: "assistant", Content: "Lions are big cats with golden manes."}
			if calls.Add(1) > 1 {
				msg.Content = choice
			}
			_ = json.NewEncoder(w).Encode(api.ChatResponse{Message: msg, Done: true, DoneReason: "stop"})
		}
	}))
	defer server.Close()
	cfg := &config.Config{OllamaHost: server.URL, OllamaModel: "live", VoiceToolsEnabled: true, AgentName: "Obey", MaxHistory: 20, WorkDir: root, PromptsDir: filepath.Join(root, "prompts"), SkillsDir: filepath.Join(root, "skills")}
	live, err := brain.NewOllama(cfg)
	if err != nil {
		t.Fatal(err)
	}
	provider := &groupingFakeTTS{}
	player := newFakePlayer(time.Millisecond)
	defer player.Close()
	bus := events.NewBus()
	events.Subscribe(bus, func(e events.Error) { errs = append(errs, e.Message) })
	p := &Pipeline{Brain: live, TTS: provider, Player: player, Events: bus}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	stream, err := live.ThinkStream(ctx, "Show me a picture of a lion in the chat.", brain.StreamOptions{ToolsEnabled: true, RouterToolsPolicy: brain.RouterToolsDirect})
	if err != nil {
		t.Fatal(err)
	}
	metrics := newTurnMetrics()
	response, _, err = p.streamResponse(ctx, cancel, stream, false, metrics, p.newTurnConductor(metrics), nil)
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Fatalf("chat requests = %d", calls.Load())
	}
	return provider.Texts(), response, errs, metrics.degraded, p.finalizeMedia(metrics, response)
}

func TestChosenPictureSpeaksOnlyItsCaption(t *testing.T) {
	spoken, response, errs, degraded, reply := pictureRetryTurn(t, `{"wanted":"a lion","file":"lion.jpg","caption":"Here is a lion in the grass."}`)
	if strings.Join(spoken, " ") != "Here is a lion in the grass." || degraded || len(errs) != 0 {
		t.Fatalf("spoken=%q degraded=%t errors=%q", spoken, degraded, errs)
	}
	if !strings.Contains(response, "![lion.jpg](<lion.jpg>)") || len(reply.Attachments) != 1 || reply.Attachments[0].Name != "lion.jpg" || reply.Response != "Here is a lion in the grass.\nlion.jpg" {
		t.Fatalf("the reply lost its picture: %q -> %q with %d attachments", response, reply.Response, len(reply.Attachments))
	}
}

func TestUnusablePictureChoiceSpeaksTheNoPictureLine(t *testing.T) {
	spoken, response, errs, degraded, reply := pictureRetryTurn(t, "Here is a lion!")
	const line = "I couldn't put a picture in the chat this time."
	if strings.Join(spoken, " ") != line || response != line {
		t.Fatalf("spoken=%q response=%q", spoken, response)
	}
	if !degraded || len(errs) != 1 || !strings.Contains(errs[0], "no image was attached") || len(reply.Attachments) != 0 {
		t.Fatalf("the failure is not shown: degraded=%t errors=%q", degraded, errs)
	}
}

// A native-audio persona speaks no failed partial, but the no-picture line is
// the host's answer, so it is spoken there too, beside the error.
func TestNativePersonaSpeaksTheNoPictureLine(t *testing.T) {
	const line = "I couldn't put a picture in the chat this time."
	player := newFakePlayer(time.Millisecond)
	defer player.Close()
	provider := &groupingFakeTTS{}
	bus := events.NewBus()
	var errs atomic.Int32
	events.Subscribe(bus, func(events.Error) { errs.Add(1) })
	p := &Pipeline{Native: &NativePolicy{}, TTS: provider, Player: player, Events: bus}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	chunks := make(chan string, 1)
	chunks <- line
	close(chunks)
	terminal := make(chan brain.StreamResult, 1)
	terminal <- brain.StreamResult{Err: fmt.Errorf("%w: the reply contained no standalone image reference", brain.ErrNoPictureAttached), Recovered: true}
	close(terminal)
	metrics := newTurnMetrics()
	response, _, err := p.streamResponse(ctx, cancel, &brain.Stream{Chunks: chunks, Done: terminal}, false, metrics, p.newTurnConductor(metrics), nil)
	if err != nil || response != line || strings.Join(provider.Texts(), " ") != line {
		t.Fatalf("response=%q spoken=%q err=%v", response, provider.Texts(), err)
	}
	if !metrics.degraded || errs.Load() != 1 {
		t.Fatalf("degraded=%t errors=%d", metrics.degraded, errs.Load())
	}
}
