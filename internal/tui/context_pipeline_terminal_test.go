package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/pipeline"
	tea "github.com/charmbracelet/bubbletea"
)

// The original meter tests bypassed the bus bridge. Exercise the real Ollama
// protocol -> pipeline -> bus -> bridge -> Bubble Tea renderer instead.
func TestContextMeterReceivesLivePipelineUsage(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	finish := make(chan struct{})
	var finishOnce sync.Once
	release := func() { finishOnce.Do(func() { close(finish) }) }
	defer release()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/tags" {
			_, _ = fmt.Fprint(w, `{"models":[{"name":"m"}]}`)
			return
		}
		if r.URL.Path != "/api/chat" {
			t.Errorf("unexpected model request: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/x-ndjson")
		_ = json.NewEncoder(w).Encode(map[string]any{"message": map[string]string{"role": "assistant", "content": strings.Repeat("A traveler told a story. ", 64)}})
		w.(http.Flusher).Flush()
		select {
		case <-finish:
		case <-r.Context().Done():
			return
		}
		_, _ = fmt.Fprintln(w, `{"message":{"role":"assistant","content":"The end."},"done":true,"done_reason":"stop"}`)
	}))
	defer func() {
		release()
		srv.Close()
	}()
	cfg := &config.Config{BrainProvider: "ollama", OllamaHost: srv.URL, OllamaModel: "m", OllamaNumCtx: 32768, MaxHistory: 40, PromptsDir: t.TempDir(), WorkDir: t.TempDir()}
	b, err := brain.NewOllama(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = brain.CloseProvider(b) }()
	bus := events.NewBus()
	m := sizedConversation(t, 120, 28)
	m.cfg, m.deps.ctx = cfg, ctx
	m.bridge = newEventBridge(32)
	m.bridge.attach(bus)
	h := &contextTerminalHarness{model: m, views: make(chan string, 128), budgets: make(chan events.ContinuityBudget, 16)}
	p := tea.NewProgram(h, tea.WithInput(nil), tea.WithOutput(h), tea.WithContext(ctx), tea.WithoutSignalHandler())
	uiDone := make(chan error, 1)
	go func() { _, err := p.Run(); uiDone <- err }()
	defer func() {
		p.Quit()
		if err := <-uiDone; err != nil {
			t.Error(err)
		}
	}()
	pipe := &pipeline.Pipeline{Brain: b, Events: bus}
	pipe.SetOutputMuted(true)
	turnDone := make(chan error, 1)
	go func() { turnDone <- pipe.RunTurnTextMode(ctx, "Tell me a story.") }()
	budget := func() events.ContinuityBudget {
		t.Helper()
		select {
		case e := <-h.budgets:
			return e
		case <-ctx.Done():
			t.Fatal("usage did not reach the TUI through the pipeline bus")
			return events.ContinuityBudget{}
		}
	}
	first, during := budget(), budget()
	if first.EstTokens <= 0 || during.EstTokens < first.EstTokens+64 {
		t.Fatalf("usage did not grow before generation finished: first=%+v during=%+v", first, during)
	}
	// Verify the actual terminal flush while the server is still generating.
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		h.mu.Lock()
		output := stripANSI(h.output.String())
		h.mu.Unlock()
		if strings.Contains(output, "Context ~"+contextTokens(during.EstTokens)) {
			break
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			t.Fatal("live usage never reached the terminal renderer")
		}
	}
	release()
	select {
	case err := <-turnDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("pipeline did not finish")
	}
	final := budget()
	if final.EstTokens != b.SessionInfo().PromptTokens || final.EstTokens <= first.EstTokens {
		t.Fatalf("final meter does not reflect retained reply: %+v; session=%+v", final, b.SessionInfo())
	}
	for len(h.views) > 0 {
		view := <-h.views
		if strings.Contains(view, "Context ~"+contextTokens(final.EstTokens)) {
			t.Logf("Final terminal frame:\n%s", view)
			return
		}
	}
	t.Fatal("final usage missing from terminal frame")
}
