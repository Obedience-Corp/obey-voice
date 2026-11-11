package brain

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/ollama/ollama/api"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/skills"
)

type embedServer struct {
	*httptest.Server
	catalogCalls atomic.Int32
}

func newEmbedServer(t *testing.T) *embedServer {
	t.Helper()
	s := &embedServer{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req api.EmbedRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		inputs, ok := req.Input.([]any)
		if !ok {
			inputs = []any{req.Input}
		} else {
			s.catalogCalls.Add(1)
		}
		vectors := make([][]float32, len(inputs))
		for i := range inputs {
			vectors[i] = []float32{1, float32(i)}
		}
		_ = json.NewEncoder(w).Encode(api.EmbedResponse{Model: req.Model, Embeddings: vectors})
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *embedServer) client(t *testing.T) *api.Client {
	t.Helper()
	base, err := url.Parse(s.URL)
	if err != nil {
		t.Fatal(err)
	}
	return api.NewClient(base, s.Client())
}

func TestSharedSkillRouterEmbedsCatalogOncePerKey(t *testing.T) {
	srv := newEmbedServer(t)
	client := srv.client(t)
	catalog := routingCatalog()
	build := func(host, model string, catalog []skills.Skill) {
		t.Helper()
		router, err := newSharedSemanticSkillRouter(context.Background(), host, client, model, 0.55, catalog, nil)
		if err != nil || router == nil || len(router.vectors) != len(catalog) {
			t.Fatalf("router = %v, err = %v", router, err)
		}
	}

	host := srv.URL + "/" + t.Name()
	build(host, "embed-a", catalog)
	build(host, "embed-a", catalog)
	if got := srv.catalogCalls.Load(); got != 1 {
		t.Fatalf("catalog embeds after identical builds = %d, want 1", got)
	}

	changed := append([]skills.Skill(nil), catalog...)
	changed[1].Description = "Edit PDF documents"
	build(host, "embed-a", changed)
	if got := srv.catalogCalls.Load(); got != 2 {
		t.Fatalf("catalog embeds after catalog change = %d, want 2", got)
	}

	build(host, "embed-b", catalog)
	if got := srv.catalogCalls.Load(); got != 3 {
		t.Fatalf("catalog embeds after model change = %d, want 3", got)
	}

	build(host+"-other", "embed-a", catalog)
	if got := srv.catalogCalls.Load(); got != 4 {
		t.Fatalf("catalog embeds after host change = %d, want 4", got)
	}

	body := append([]skills.Skill(nil), catalog...)
	body[0].Body = "different instructions"
	build(host, "embed-a", body)
	if got := srv.catalogCalls.Load(); got != 4 {
		t.Fatalf("body-only change re-embedded: %d, want 4", got)
	}
}

func TestSharedSkillRouterConcurrentBuildsEmbedOnce(t *testing.T) {
	srv := newEmbedServer(t)
	client := srv.client(t)
	host := srv.URL + "/" + t.Name()
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := newSharedSemanticSkillRouter(context.Background(), host, client, "embed", 0.55, routingCatalog(), nil); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if got := srv.catalogCalls.Load(); got != 1 {
		t.Fatalf("catalog embeds = %d, want 1", got)
	}
}

func TestSharedSkillRouterDoesNotCacheFailures(t *testing.T) {
	var fail atomic.Bool
	fail.Store(true)
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if fail.Load() {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		vectors := [][]float32{{1}, {1}, {1}}
		_ = json.NewEncoder(w).Encode(api.EmbedResponse{Embeddings: vectors})
	}))
	defer srv.Close()
	base, _ := url.Parse(srv.URL)
	client := api.NewClient(base, srv.Client())
	host := srv.URL + "/" + t.Name()

	if _, err := newSharedSemanticSkillRouter(context.Background(), host, client, "embed", 0.55, routingCatalog(), nil); err == nil {
		t.Fatal("want error from failing server")
	}
	fail.Store(false)
	router, err := newSharedSemanticSkillRouter(context.Background(), host, client, "embed", 0.55, routingCatalog(), nil)
	if err != nil || router == nil {
		t.Fatalf("retry after failure: router = %v, err = %v", router, err)
	}
}
