package pipeline

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/internal/transcript"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/job"
	"github.com/ollama/ollama/api"
)

// rewordedListings each print the same listing, worded differently.
var rewordedListings = []string{
	"ls", "ls -1", "ls | cat", "ls -1 | cat", "ls .", "ls ./", "ls -- .", "command ls",
	"ls | sort", "ls | cat | cat", "ls -1 .", "ls | head -n 100", "ls -1 ./", "ls -1 -- .",
}

func listingCall(command string) api.ChatResponse {
	args := api.NewToolCallFunctionArguments()
	args.Set("command", command)
	return api.ChatResponse{Done: true, DoneReason: "stop", Message: api.Message{Role: "assistant", ToolCalls: []api.ToolCall{{Function: api.ToolCallFunction{Name: "run_command", Arguments: args}}}}}
}

// A worker that lists one folder round after round, rewording the command,
// is never stopped for it. /workers and the persona's job list say how many
// rounds returned the same listing, the job keeps running, a cancel still
// ends it, and the count leaves with the job.
func TestRewordedListingJobShowsSameOutputAndStillCancels(t *testing.T) {
	config.SetConfigDirForTest(t, t.TempDir())
	root, workDir := t.TempDir(), t.TempDir()
	for _, name := range []string{"a.txt", "b.txt", "notes.md"} {
		if err := os.WriteFile(filepath.Join(workDir, name), []byte(name), 0600); err != nil {
			t.Fatal(err)
		}
	}
	const pauseAt = 12
	paused := make(chan struct{})
	var mu sync.Mutex
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tags":
			_, _ = w.Write([]byte(`{"models":[{"name":"fixture"}]}`))
			return
		case "/api/show":
			_, _ = w.Write([]byte(`{"capabilities":["completion","tools"],"details":{"family":"fixture"}}`))
			return
		}
		var req api.ChatRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		mu.Lock()
		requests++
		n := requests
		mu.Unlock()
		if len(req.Tools) == 0 {
			t.Errorf("request %d withheld tools", n)
		}
		if n >= pauseAt {
			if n == pauseAt {
				close(paused)
			}
			<-r.Context().Done()
			return
		}
		_ = json.NewEncoder(w).Encode(listingCall(rewordedListings[(n-1)%len(rewordedListings)]))
	}))
	t.Cleanup(server.Close)
	cfg := &config.Config{OllamaHost: server.URL, OllamaModel: "fixture", WorkDir: workDir, PromptsDir: filepath.Join(root, "prompts"), MaxHistory: 100, OllamaNumCtx: 32768, VoiceWorkerProvider: "ollama", VoiceWorkerModel: "fixture", VoiceToolsEnabled: true}
	workerCfg := cfg.Clone()
	workerCfg.IsWorker = true
	worker, err := brain.NewOllama(workerCfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = worker.Close() })
	live, err := brain.NewOllama(&config.Config{OllamaHost: server.URL, OllamaModel: "fixture", WorkDir: workDir, PromptsDir: filepath.Join(root, "prompts")})
	if err != nil {
		t.Fatal(err)
	}
	p := &Pipeline{Brain: live, Events: events.NewBus()}
	p.SetOutputMuted(true)
	if err := p.BeginConversationIdentity("conversation-1"); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(root, "transcript.jsonl")
	log, err := transcript.NewWriter(logPath)
	if err != nil {
		t.Fatal(err)
	}
	log.Attach(p.Events)
	table := job.NewTable(t.Context())
	t.Cleanup(table.Close)
	a := NewAgency(p, table, worker, cfg, nil)
	a.Wire()
	aborted := make(chan events.JobAborted, 1)
	ended := make(chan string, 2)
	events.Subscribe(p.Events, func(e events.JobAborted) { aborted <- e })
	events.Subscribe(p.Events, func(e events.JobFinished) { ended <- "finished" })
	events.Subscribe(p.Events, func(e events.JobFailed) { ended <- "failed: " + e.Err })
	metrics := newTurnMetrics()
	a.HandleRoute(t.Context(), p.newTurnConductor(metrics), metrics, RouteDecision{Kind: RouteSpawn, Transcript: "List the files in the project folder"})

	select {
	case <-paused:
	case outcome := <-ended:
		t.Fatalf("the job ended on its own: %s", outcome)
	case <-time.After(10 * time.Second):
		t.Fatal("the job never reached its twelfth request")
	}
	const want = "same output for 11 rounds"
	workers := p.Workers()
	if len(workers) != 1 || workers[0].State != "running" || brain.SameOutputNote(workers[0].SameOutput) != want {
		t.Fatalf("/workers = %+v", workers)
	}
	prompt := brain.WorkerFactsPrompt(nil, p.workerFacts(true))
	if !strings.Contains(prompt, ` output="`+want+`"`) {
		t.Fatalf("the persona's job list lacks the note: %q", prompt)
	}

	if err := p.CancelWork(workers[0].ID); err != nil {
		t.Fatal(err)
	}
	select {
	case <-aborted:
	case outcome := <-ended:
		t.Fatalf("cancel did not end the job as cancelled: %s", outcome)
	case <-time.After(10 * time.Second):
		t.Fatal("cancel did not end the job")
	}
	deadline := time.Now().Add(5 * time.Second)
	for table.Active() {
		if time.Now().After(deadline) {
			t.Fatal("the cancelled worker never returned")
		}
		time.Sleep(5 * time.Millisecond)
	}
	for _, info := range p.Workers() {
		if info.SameOutput != 0 {
			t.Fatalf("an ended job still shows its count: %+v", info)
		}
	}
	a.mu.Lock()
	left := a.workerOrigins[workers[0].ID].sameOutput
	a.mu.Unlock()
	if left != 0 {
		t.Fatalf("the count outlived the job: %d", left)
	}
	if err := log.Close(); err != nil {
		t.Fatal(err)
	}
	saved, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(saved), "same output") {
		t.Fatalf("the count was saved to the conversation: %s", saved)
	}
}
