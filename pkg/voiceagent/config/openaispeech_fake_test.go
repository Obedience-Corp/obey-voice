package config

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// fakeOllama serves the slice of Ollama's API the openai-speech seam reads.
type fakeOllama struct {
	version string
	// tags is what /api/tags lists; nil answers 404.
	tags []string
	// voices is /api/show's list; showStatus overrides the 200.
	voices     []string
	showStatus int
	// pull writes the /api/pull stream; nil answers {"status":"success"}.
	pull func(w http.ResponseWriter, r *http.Request)

	mu        sync.Mutex
	pulls     []map[string]any
	notOllama bool
	// auth is the Authorization header each path last carried.
	auth map[string]string
}

func (f *fakeOllama) authOn(path string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.auth[path]
}

func (f *fakeOllama) pullBodies() []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]map[string]any(nil), f.pulls...)
}

func (f *fakeOllama) start(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/version", func(w http.ResponseWriter, r *http.Request) {
		if f.notOllama {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"version": f.version})
	})
	mux.HandleFunc("/api/tags", func(w http.ResponseWriter, r *http.Request) {
		if f.tags == nil || f.notOllama {
			http.NotFound(w, r)
			return
		}
		models := []map[string]string{}
		for _, n := range f.tags {
			models = append(models, map[string]string{"name": n, "model": n})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"models": models})
	})
	mux.HandleFunc("/v1/models", func(w http.ResponseWriter, r *http.Request) {
		data := []map[string]string{}
		for _, n := range f.tags {
			data = append(data, map[string]string{"id": n})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": data})
	})
	mux.HandleFunc("/api/show", func(w http.ResponseWriter, r *http.Request) {
		if f.showStatus != 0 {
			w.WriteHeader(f.showStatus)
			return
		}
		doc := map[string]any{"details": map[string]string{}}
		if f.voices != nil {
			doc["voices"] = f.voices
		}
		_ = json.NewEncoder(w).Encode(doc)
	})
	mux.HandleFunc("/api/pull", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		f.pulls = append(f.pulls, body)
		f.mu.Unlock()
		if f.pull != nil {
			f.pull(w, r)
			return
		}
		fmt.Fprintln(w, `{"status":"success"}`)
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		if f.auth == nil {
			f.auth = map[string]string{}
		}
		f.auth[r.URL.Path] = r.Header.Get("Authorization")
		f.mu.Unlock()
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// fakeVoicesURL points the voice catalog download at srv for the test.
func fakeVoicesURL(t *testing.T, url string) {
	t.Helper()
	orig := openAISpeechVoicesURL
	openAISpeechVoicesURL = url
	t.Cleanup(func() { openAISpeechVoicesURL = orig })
}

// fakeCatalog serves body as voices.jsonl and counts requests.
func fakeCatalog(t *testing.T, status int, body string) (*httptest.Server, *int) {
	t.Helper()
	var mu sync.Mutex
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits++
		mu.Unlock()
		w.WriteHeader(status)
		fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)
	fakeVoicesURL(t, srv.URL+"/voices.jsonl")
	return srv, &hits
}

// closedURL is an address nothing listens on.
func closedURL(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()
	return url
}

func openAISpeechCfg(t *testing.T, base string) *Config {
	t.Helper()
	return &Config{TTSProvider: OpenAISpeechProviderName, OpenAISpeechURL: base, ModelsDir: t.TempDir()}
}

func voicesPath(cfg *Config) string {
	return filepath.Join(ModelsDirFrom(cfg), filepath.FromSlash(OpenAISpeechVoicesFile))
}

func writeVoicesFile(t *testing.T, cfg *Config) {
	t.Helper()
	path := voicesPath(cfg)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"name":"Zoey"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// rejectingServer answers every request 401, the way an auth proxy in front
// of Ollama does without the right key.
func rejectingServer(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}
