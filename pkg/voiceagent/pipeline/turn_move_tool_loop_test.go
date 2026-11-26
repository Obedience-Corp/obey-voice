package pipeline

import (
	"encoding/json"
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
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/job"
	"github.com/ollama/ollama/api"
)

// A live turn moved to a background job keeps running its own tool loop.
// Cancelling that job, as /work cancel does, stops the loop where it is: the
// running command is killed, the rest of its round does not run, and no
// further model request is sent.
func TestCancellingAMovedTurnStopsItsToolLoop(t *testing.T) {
	dir := t.TempDir()
	runs := filepath.Join(dir, "runs.txt")
	command := func(cmd string) api.ToolCall {
		args := api.NewToolCallFunctionArguments()
		args.Set("command", cmd)
		return api.ToolCall{Function: api.ToolCallFunction{Name: "run_command", Arguments: args}}
	}
	write := func(path string) api.ToolCall {
		args := api.NewToolCallFunctionArguments()
		args.Set("path", path)
		args.Set("content", "built")
		return api.ToolCall{Function: api.ToolCallFunction{Name: "write_file", Arguments: args}}
	}
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/tags" {
			_ = json.NewEncoder(w).Encode(api.ListResponse{Models: []api.ListModelResponse{{Name: "fixture", Model: "fixture"}}})
			return
		}
		if r.URL.Path != "/api/chat" {
			http.NotFound(w, r)
			return
		}
		var calls []api.ToolCall
		switch requests.Add(1) {
		case 1:
			calls = []api.ToolCall{command("echo one >> runs.txt")}
		case 2:
			calls = []api.ToolCall{command("echo two >> runs.txt"), command("sleep 5"), write("three.txt")}
		default:
			calls = []api.ToolCall{command("echo later >> runs.txt")}
		}
		_ = json.NewEncoder(w).Encode(api.ChatResponse{Done: true, DoneReason: "stop", Message: api.Message{Role: "assistant", ToolCalls: calls}})
	}))
	defer server.Close()
	cfg := &config.Config{OllamaHost: server.URL, OllamaModel: "fixture", OllamaNumCtx: 32768, MaxHistory: 40, PromptsDir: t.TempDir(), WorkDir: dir, VoiceToolsEnabled: true, ToolCommandTimeout: 30, AgentName: "Anika", ActivePersona: "anika"}
	b, err := brain.NewOllama(cfg)
	if err != nil {
		t.Fatal(err)
	}
	p := &Pipeline{Brain: b, Events: events.NewBus(), VoiceToolsEnabled: true}
	if err := p.BeginConversationIdentity("s"); err != nil {
		t.Fatal(err)
	}
	p.AdmitInput = admitPersona("anika", "Anika")
	p.NamesOtherPersona = func(input string) bool { return strings.HasPrefix(input, "Hey Obey") }
	jobs := job.NewTable(t.Context())
	t.Cleanup(jobs.Close)
	NewAgency(p, jobs, nil, cfg, nil).Wire()
	aborted := make(chan struct{}, 1)
	events.Subscribe(p.Events, func(events.JobAborted) {
		select {
		case aborted <- struct{}{}:
		default:
		}
	})

	turn := make(chan error, 1)
	go func() { turn <- p.RunTurnTextMode(t.Context(), "Anika, run the build steps") }()
	deadline := time.Now().Add(10 * time.Second)
	for {
		data, _ := os.ReadFile(runs)
		if strings.Contains(string(data), "two") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the second round never started: %q", data)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !p.MoveTurnToBackground("Hey Obey, what time is it?") {
		t.Fatal("the running turn was not moved to the background")
	}
	if err := <-turn; err != nil {
		t.Fatalf("moved turn returned %v", err)
	}
	var id string
	for _, info := range jobs.Status() {
		if info.State == job.StateRunning {
			id = info.ID
		}
	}
	if id == "" {
		t.Fatalf("no job runs the moved turn: %+v", jobs.Status())
	}
	cancelled := time.Now()
	if err := p.CancelWork(id); err != nil {
		t.Fatal(err)
	}
	select {
	case <-aborted:
	case <-time.After(5 * time.Second):
		t.Fatal("the moved turn's job was not aborted")
	}
	p.requestMu.Lock()
	rec := p.requests[len(p.requests)-1]
	p.requestMu.Unlock()
	select {
	case <-rec.drained:
	case <-time.After(10 * time.Second):
		t.Fatal("the moved request did not end")
	}
	if took := time.Since(cancelled); took > 4*time.Second {
		t.Fatalf("the running command was not stopped: the request took %v to end", took)
	}
	data, _ := os.ReadFile(runs)
	if got := strings.Fields(string(data)); len(got) != 2 || got[0] != "one" || got[1] != "two" {
		t.Fatalf("commands that ran: %q", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "three.txt")); err == nil {
		t.Fatal("a tool call after the cancelled one ran")
	}
	if n := requests.Load(); n != 2 {
		t.Fatalf("model requests = %d, want none after the cancel", n)
	}
}
