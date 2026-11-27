package pipeline

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/job"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/ollamamem"
)

func TestLocalModelResourceKeys(t *testing.T) {
	key := func(cfg *config.Config) string {
		model, ok := workerLocalModel(cfg)
		if !ok {
			return ""
		}
		return model.resource()
	}
	local := key(&config.Config{VoiceWorkerProvider: "ollama", VoiceWorkerModel: "example-model:7b"})
	if local == "" {
		t.Fatal("a local Ollama worker has no resource key")
	}
	for _, cfg := range []*config.Config{
		{VoiceWorkerProvider: "ollama", VoiceWorkerModel: "example-model:7b", OllamaHost: "http://localhost:11434"},
		{VoiceWorkerProvider: "ollama", VoiceWorkerModel: "example-model:7b", OllamaHost: "http://remote.example:11434", VoiceWorkerOllamaHost: "http://127.0.0.1:11434/"},
		{VoiceWorkerProvider: "OLLAMA", VoiceWorkerModel: "Example-Model:7b", OllamaHost: "localhost"},
	} {
		if got := key(cfg); got != local {
			t.Errorf("key(%+v) = %q, want %q", cfg, got, local)
		}
	}
	if other := key(&config.Config{VoiceWorkerProvider: "ollama", VoiceWorkerModel: "local-model-b"}); other == local {
		t.Error("two models share a key")
	}
	if remote := key(&config.Config{VoiceWorkerProvider: "ollama", VoiceWorkerModel: "example-model:7b", VoiceWorkerOllamaHost: "http://gpu-box.example:11434"}); remote == local || remote == "" {
		t.Errorf("remote host key = %q", remote)
	}
	for _, provider := range []string{"claude", "codex", "grok", "obey"} {
		if got := key(&config.Config{VoiceWorkerProvider: provider, VoiceWorkerModel: "some-model"}); got != "" {
			t.Errorf("%s worker key = %q, want none", provider, got)
		}
	}
	brain, ok := brainLocalModel(&config.Config{BrainProvider: "ollama", OllamaModel: "example-model:7b", OllamaHost: "http://127.0.0.1:11434"})
	if !ok || brain.resource() != local {
		t.Errorf("brain key = %q, want the worker's %q for the same model", brain.resource(), local)
	}
}

// fakeOllamaLists serves /api/ps and /api/tags.
type fakeOllamaLists struct {
	mu     sync.Mutex
	loaded map[string]int64
	onDisk map[string]int64
}

func (f *fakeOllamaLists) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	sizes := f.onDisk
	if r.URL.Path == "/api/ps" {
		sizes = f.loaded
	}
	models := []map[string]any{}
	for name, size := range sizes {
		models = append(models, map[string]any{"name": name, "model": name, "size": size})
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"models": models})
}

func TestLocalMemoryHoldsAJobUntilItsModelFits(t *testing.T) {
	const gb = 1_000_000_000
	server := httptest.NewServer(&fakeOllamaLists{
		loaded: map[string]int64{"local-model-a": 9 * gb, "local-model-b": 9 * gb},
		onDisk: map[string]int64{"local-model-a": 5 * gb, "local-model-b": 5 * gb, "local-model-c": 30 * gb},
	})
	defer server.Close()
	var free atomic.Uint64
	free.Store(16 * gb)
	monitor := ollamamem.New()
	monitor.FreeMemory = func(context.Context) (uint64, bool) { return free.Load(), true }

	jobs := job.NewTable(context.Background(), job.WithMaxConcurrent(4))
	t.Cleanup(jobs.AbortAll)
	a := &Agency{jobs: jobs}
	stop := a.UseLocalMemory(context.Background(), monitor)
	defer stop()
	jobs.OnDone(func(string, job.State, string, error) { a.jobEndedForMemory() })

	release := map[string]chan struct{}{}
	started := make(chan string, 4)
	start := func(task, model string) {
		t.Helper()
		release[task] = make(chan struct{})
		gate := release[task]
		spec := job.Spec{Task: task, Run: func(ctx context.Context) (string, error) {
			started <- task
			select {
			case <-gate:
				return "", nil
			case <-ctx.Done():
				return "", ctx.Err()
			}
		}}
		local, ok := localModelOf("ollama", server.URL, model)
		a.placeJob(context.Background(), &spec, local, ok)
		if _, err := jobs.Start(context.Background(), spec); err != nil {
			t.Fatal(err)
		}
	}
	await := func(want string) {
		t.Helper()
		select {
		case got := <-started:
			if got != want {
				t.Fatalf("started %q, want %q", got, want)
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("%s never started", want)
		}
	}
	state := func(task string) job.Info {
		for _, info := range jobs.Status() {
			if info.Task == task {
				return info
			}
		}
		t.Fatalf("no job %s", task)
		return job.Info{}
	}

	start("first", "local-model-a")
	await("first")
	start("second", "local-model-b")
	await("second")
	start("third", "local-model-c")
	if info := state("third"); info.State != job.StateQueued || info.WaitKind != job.WaitAdmit || info.Waiting != "waiting for memory (needs about 30 GB, 16 GB free)" {
		t.Fatalf("third = %+v", info)
	}
	start("same model", "local-model-a")
	if info := state("same model"); info.State != job.StateQueued || info.Waiting != "same model busy" {
		t.Fatalf("same-model job = %+v", info)
	}

	// Memory frees up; a job ending refreshes the readings, and the held job
	// starts beside the one still running.
	free.Store(40 * gb)
	close(release["second"])
	await("third")
	if info := state("first"); info.State != job.StateRunning {
		t.Fatalf("first = %+v", info)
	}
	close(release["first"])
	await("same model")
}

func TestRemoteOllamaJobGetsAKeyButNoMemoryGate(t *testing.T) {
	jobs := job.NewTable(context.Background())
	a := &Agency{jobs: jobs}
	stop := a.UseLocalMemory(context.Background(), ollamamem.New())
	defer stop()
	var spec job.Spec
	model, ok := localModelOf("ollama", "http://gpu-box.example:11434", "local-model-a")
	a.placeJob(context.Background(), &spec, model, ok)
	if spec.Resource == "" || spec.Admit != nil {
		t.Fatalf("remote job: resource %q, gated %v", spec.Resource, spec.Admit != nil)
	}
}

func TestMemoryRecheckTickerStartsAHeldJobAndStops(t *testing.T) {
	const gb = 1_000_000_000
	server := httptest.NewServer(&fakeOllamaLists{
		loaded: map[string]int64{"local-model-a": 9 * gb},
		onDisk: map[string]int64{"local-model-a": 5 * gb, "local-model-c": 30 * gb, "local-model-d": 30 * gb},
	})
	defer server.Close()
	var free atomic.Uint64
	free.Store(16 * gb)
	monitor := ollamamem.New()
	monitor.FreeMemory = func(context.Context) (uint64, bool) { return free.Load(), true }
	jobs := job.NewTable(context.Background(), job.WithMaxConcurrent(4))
	t.Cleanup(jobs.AbortAll)
	a := &Agency{jobs: jobs}
	stop := a.UseLocalMemory(context.Background(), monitor)
	defer stop()
	m := a.localMemory()
	m.every = 10 * time.Millisecond

	started := make(chan string, 4)
	start := func(task, model string) {
		t.Helper()
		spec := job.Spec{Task: task, Run: func(ctx context.Context) (string, error) {
			started <- task
			<-ctx.Done()
			return "", ctx.Err()
		}}
		local, ok := localModelOf("ollama", server.URL, model)
		a.placeJob(context.Background(), &spec, local, ok)
		if _, err := jobs.Start(context.Background(), spec); err != nil {
			t.Fatal(err)
		}
	}
	ticking := func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		return m.ticking
	}
	eventually := func(what string, cond func() bool) {
		t.Helper()
		deadline := time.Now().Add(3 * time.Second)
		for !cond() {
			if time.Now().After(deadline) {
				t.Fatal(what)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}

	start("first", "local-model-a")
	<-started
	start("held", "local-model-c")
	if !ticking() {
		t.Fatal("a job held for memory did not start the recheck ticker")
	}
	free.Store(40 * gb)
	select {
	case got := <-started:
		if got != "held" {
			t.Fatalf("started %q", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the ticker never started the held job once memory freed")
	}
	eventually("the ticker kept running with no job held for memory", func() bool { return !ticking() })

	free.Store(1 * gb)
	start("held again", "local-model-d")
	if !ticking() {
		t.Fatal("a second hold did not restart the ticker")
	}
	stop()
	eventually("the ticker outlived stop", func() bool { return !ticking() })
}
