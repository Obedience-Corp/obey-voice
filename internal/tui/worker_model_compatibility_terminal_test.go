package tui

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/ollama/ollama/api"
)

func TestWorkerModelCompatibilityErrorTerminal(t *testing.T) {
	config.SetConfigDirForTest(t, t.TempDir())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/tags" {
			_, _ = w.Write([]byte(`{"models":[{"name":"chat-only"}]}`))
			return
		}
		var req api.ChatRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		if len(req.Tools) != 0 {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"model does not support tools"}`))
			return
		}
		_ = json.NewEncoder(w).Encode(api.ChatResponse{Message: api.Message{Role: "assistant", Content: "It is sunny in Denver."}, Done: true})
	}))
	defer srv.Close()
	b, err := brain.NewOllama(&config.Config{OllamaHost: srv.URL, OllamaModel: "chat-only", WorkDir: t.TempDir(), PromptsDir: t.TempDir(), SkillsDir: t.TempDir(), MaxHistory: 10})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	stream, err := b.ThinkStream(ctx, "Spin up a worker to look up the weather in Denver", brain.StreamOptions{ToolsEnabled: true, RouterToolsPolicy: brain.RouterToolsRouteOnly})
	if err != nil {
		t.Fatal(err)
	}
	var spoken strings.Builder
	for chunk := range stream.Chunks {
		spoken.WriteString(chunk)
	}
	result := <-stream.Done
	if result.Err == nil || spoken.Len() != 0 {
		t.Fatalf("unsupported model answered instead of reporting incompatibility: %+v / %q", result, spoken.String())
	}
	m := sizedConversation(t, 120, 30)
	m.handleEvent(events.Error{Stage: "brain", Message: result.Err.Error()})
	h := &workerCompatibilityTerminal{model: m}
	var output bytes.Buffer
	_, err = tea.NewProgram(h, tea.WithContext(ctx), tea.WithInput(nil), tea.WithOutput(&output), tea.WithoutSignalHandler()).Run()
	if err != nil {
		t.Fatal(err)
	}
	rendered := strings.Join(strings.Fields(stripANSI(output.String())), " ")
	for _, want := range []string{"Error:", "chat-only", "background workers", "Settings > Brain"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("terminal hides %q: %s", want, rendered)
		}
	}
	if strings.Contains(rendered, "sunny in Denver") {
		t.Fatal("terminal displayed an ungrounded weather reply")
	}
}

type workerCompatibilityTerminal struct{ model conversationModel }

func (*workerCompatibilityTerminal) Init() tea.Cmd {
	return tea.Tick(40*time.Millisecond, func(time.Time) tea.Msg { return tea.Quit() })
}
func (m *workerCompatibilityTerminal) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	m.model, cmd = m.model.Update(msg)
	return m, cmd
}
func (m *workerCompatibilityTerminal) View() string { return m.model.View() }
