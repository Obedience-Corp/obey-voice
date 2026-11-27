package ollamamem

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

const gb = 1_000_000_000

func TestNormalizeHost(t *testing.T) {
	for _, tc := range []struct {
		in       string
		want     string
		loopback bool
	}{
		{"", "http://localhost:11434", true},
		{"http://localhost:11434", "http://localhost:11434", true},
		{"http://127.0.0.1:11434/", "http://localhost:11434", true},
		{"localhost", "http://localhost:11434", true},
		{"HTTP://LocalHost", "http://localhost:11434", true},
		{"http://[::1]:11434", "http://localhost:11434", true},
		{"http://0.0.0.0:11434", "http://localhost:11434", true},
		{"http://127.0.0.1:8080", "http://localhost:8080", true},
		{"http://gpu-box.example:11434", "http://gpu-box.example:11434", false},
		{"https://ollama.example", "https://ollama.example:443", false},
	} {
		got, loopback := NormalizeHost(tc.in)
		if got != tc.want || loopback != tc.loopback {
			t.Errorf("NormalizeHost(%q) = %q, %v; want %q, %v", tc.in, got, loopback, tc.want, tc.loopback)
		}
	}
}

func monitorWith(state *hostState, free uint64, freeKnown bool) *Monitor {
	m := New()
	m.hosts = map[string]*hostState{}
	if state != nil {
		m.storeLocked(DefaultHost, state)
	}
	m.free, m.freeKnown = free, freeKnown
	return m
}

func TestDecide(t *testing.T) {
	listed := func() *hostState {
		return &hostState{ok: true,
			loaded: map[string]int64{"example-model:7b": 9 * gb},
			onDisk: map[string]int64{"example-model:7b": 5 * gb, "local-model-a": 20 * gb, "local-model-b": 4 * gb},
		}
	}
	for _, tc := range []struct {
		name       string
		monitor    *Monitor
		host       string
		model      string
		wantOK     bool
		wantReason string
	}{
		{name: "already loaded", monitor: monitorWith(listed(), 1*gb, true), model: "example-model:7b", wantOK: true},
		{name: "loaded, spelled with latest", monitor: monitorWith(&hostState{ok: true, loaded: map[string]int64{"local-model-a": 1}}, 0, true), model: "local-model-a:latest", wantOK: true},
		{name: "fits in free memory", monitor: monitorWith(listed(), 16*gb, true), model: "local-model-b", wantOK: true},
		{name: "does not fit", monitor: monitorWith(listed(), 16*gb, true), model: "local-model-a", wantReason: "waiting for memory (needs about 20 GB, 16 GB free)"},
		{name: "free memory unknown", monitor: monitorWith(listed(), 0, false), model: "local-model-a", wantOK: true},
		{name: "model not listed", monitor: monitorWith(listed(), 1*gb, true), model: "unlisted-model", wantOK: true},
		{name: "host unreadable", monitor: monitorWith(&hostState{}, 1*gb, true), model: "local-model-a", wantOK: true},
		{name: "never refreshed", monitor: New(), model: "local-model-a", wantOK: true},
		{name: "remote host", monitor: monitorWith(listed(), 1*gb, true), host: "http://gpu-box.example:11434", model: "local-model-a", wantOK: true},
		{name: "same host spelled differently", monitor: monitorWith(listed(), 16*gb, true), host: "127.0.0.1", model: "local-model-a", wantReason: "waiting for memory (needs about 20 GB, 16 GB free)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			host := tc.host
			if host == "" {
				host = DefaultHost
			}
			ok, reason := tc.monitor.Decide(host, tc.model)
			if ok != tc.wantOK || reason != tc.wantReason {
				t.Fatalf("Decide = %v, %q; want %v, %q", ok, reason, tc.wantOK, tc.wantReason)
			}
		})
	}
}

// fakeOllama serves /api/ps and /api/tags from fields a test can change.
type fakeOllama struct {
	mu      sync.Mutex
	loaded  map[string]int64
	onDisk  map[string]int64
	fail    bool
	methods []string
}

func (f *fakeOllama) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.methods = append(f.methods, r.Method+" "+r.URL.Path)
	if f.fail {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
		return
	}
	var models []map[string]any
	switch r.URL.Path {
	case "/api/ps":
		for name, size := range f.loaded {
			models = append(models, map[string]any{"name": name, "model": name, "size": size, "size_vram": size})
		}
	case "/api/tags":
		for name, size := range f.onDisk {
			models = append(models, map[string]any{"name": name, "model": name, "size": size})
		}
	default:
		http.NotFound(w, r)
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"models": models})
}

func TestRefreshReadsTheHostAndRemembersLoadedSizes(t *testing.T) {
	fake := &fakeOllama{
		loaded: map[string]int64{"local-model-a:latest": 30 * gb},
		onDisk: map[string]int64{"local-model-a:latest": 18 * gb, "local-model-b": 6 * gb},
	}
	server := httptest.NewServer(fake)
	defer server.Close()
	m := New()
	m.FreeMemory = func(context.Context) (uint64, bool) { return 16 * gb, true }

	m.Refresh(context.Background(), server.URL)
	if ok, _ := m.Decide(server.URL, "local-model-a"); !ok {
		t.Fatal("a loaded model was held back")
	}
	if ok, _ := m.Decide(server.URL, "local-model-b"); !ok {
		t.Fatal("a model that fits was held back")
	}

	// Unloaded, it needs what it took when loaded, not its smaller file.
	fake.mu.Lock()
	fake.loaded = nil
	fake.mu.Unlock()
	m.Refresh(context.Background(), server.URL)
	ok, reason := m.Decide(server.URL, "local-model-a")
	if ok || reason != "waiting for memory (needs about 30 GB, 16 GB free)" {
		t.Fatalf("Decide = %v, %q", ok, reason)
	}

	fake.mu.Lock()
	fake.fail = true
	fake.mu.Unlock()
	m.Refresh(context.Background(), server.URL)
	if ok, _ := m.Decide(server.URL, "local-model-a"); !ok {
		t.Fatal("an unreadable host held a job back")
	}

	fake.mu.Lock()
	defer fake.mu.Unlock()
	for _, call := range fake.methods {
		if call != "GET /api/ps" && call != "GET /api/tags" {
			t.Fatalf("probe sent %q; only list reads are allowed", call)
		}
	}
}

func TestRefreshSkipsRemoteHosts(t *testing.T) {
	m := New()
	m.FreeMemory = func(context.Context) (uint64, bool) { return 0, true }
	m.HTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		t.Errorf("probed remote host %s", r.URL)
		return nil, http.ErrUseLastResponse
	})}
	m.Refresh(context.Background(), "http://gpu-box.example:11434")
	if len(m.hosts) != 0 {
		t.Fatalf("remote host cached: %v", m.hosts)
	}
}

func TestRefreshHonoursCancellation(t *testing.T) {
	fake := &fakeOllama{onDisk: map[string]int64{"local-model-a": 30 * gb}}
	server := httptest.NewServer(fake)
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	m := New()
	m.FreeMemory = func(context.Context) (uint64, bool) { return 1, true }
	m.Refresh(ctx, server.URL)
	if ok, _ := m.Decide(server.URL, "local-model-a"); !ok {
		t.Fatal("a cancelled probe left a reading that holds jobs back")
	}
	host, _ := NormalizeHost(server.URL)
	if !strings.HasPrefix(host, "http://localhost:") {
		t.Fatalf("test server host %q is not loopback", host)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
