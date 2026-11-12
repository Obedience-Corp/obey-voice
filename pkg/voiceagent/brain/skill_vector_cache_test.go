package brain

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ollama/ollama/api"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/skills"
)

type embedServer struct {
	*httptest.Server
	catalogCalls atomic.Int32
}

func resetSharedCatalogVectors(t *testing.T) {
	t.Helper()
	sharedCatalogVectors.reset()
	t.Cleanup(sharedCatalogVectors.reset)
}

func newEmbedServer(t *testing.T) *embedServer {
	t.Helper()
	resetSharedCatalogVectors(t)
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

	host := srv.URL
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
	host := srv.URL
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
	resetSharedCatalogVectors(t)
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
	host := srv.URL

	if _, err := newSharedSemanticSkillRouter(context.Background(), host, client, "embed", 0.55, routingCatalog(), nil); err == nil {
		t.Fatal("want error from failing server")
	}
	fail.Store(false)
	router, err := newSharedSemanticSkillRouter(context.Background(), host, client, "embed", 0.55, routingCatalog(), nil)
	if err != nil || router == nil {
		t.Fatalf("retry after failure: router = %v, err = %v", router, err)
	}
}

func TestCatalogVectorCacheEvictsOnlyPopulatedEntries(t *testing.T) {
	c := &catalogVectorCache{entries: map[string]*catalogVectorEntry{}}
	ctx := context.Background()
	release := make(chan struct{})
	started := make(chan struct{})
	var loads atomic.Int32
	slow := func(context.Context) ([][]float32, error) {
		loads.Add(1)
		close(started)
		<-release
		return [][]float32{{1}}, nil
	}
	fast := func(context.Context) ([][]float32, error) { return [][]float32{{2}}, nil }

	leader := make(chan error, 1)
	go func() {
		_, err := c.get(ctx, "slow", slow)
		leader <- err
	}()
	<-started

	for i := range maxCatalogVectorEntries + 4 {
		if _, err := c.get(ctx, "k"+strconv.Itoa(i), fast); err != nil {
			t.Fatal(err)
		}
	}
	c.mu.Lock()
	_, loading := c.entries["slow"]
	size := len(c.entries)
	c.mu.Unlock()
	if !loading {
		t.Fatal("in-flight entry was evicted")
	}
	if size > maxCatalogVectorEntries {
		t.Fatalf("entries = %d, want at most %d", size, maxCatalogVectorEntries)
	}

	waiter := make(chan error, 1)
	go func() {
		_, err := c.get(ctx, "slow", slow)
		waiter <- err
	}()
	close(release)
	if err := <-leader; err != nil {
		t.Fatal(err)
	}
	if err := <-waiter; err != nil {
		t.Fatal(err)
	}
	if got := loads.Load(); got != 1 {
		t.Fatalf("slow loads = %d, want 1", got)
	}
}

func TestCatalogVectorCacheKeepsWarmEntriesAtCapacity(t *testing.T) {
	c := &catalogVectorCache{entries: map[string]*catalogVectorEntry{}}
	var loads atomic.Int32
	load := func(context.Context) ([][]float32, error) {
		loads.Add(1)
		return [][]float32{{1}}, nil
	}
	for i := range maxCatalogVectorEntries {
		if _, err := c.get(context.Background(), "k"+strconv.Itoa(i), load); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := c.get(context.Background(), "extra", load); err != nil {
		t.Fatal(err)
	}
	if got := len(c.entries); got != maxCatalogVectorEntries {
		t.Fatalf("entries = %d, want %d: eviction should drop one entry, not all", got, maxCatalogVectorEntries)
	}
}

func TestCatalogVectorCacheCancelledWaiterReturnsPromptly(t *testing.T) {
	c := &catalogVectorCache{entries: map[string]*catalogVectorEntry{}}
	release := make(chan struct{})
	started := make(chan struct{})
	leader := make(chan error, 1)
	go func() {
		_, err := c.get(context.Background(), "k", func(context.Context) ([][]float32, error) {
			close(started)
			<-release
			return [][]float32{{1}}, nil
		})
		leader <- err
	}()
	<-started

	ctx, cancel := context.WithCancel(context.Background())
	waiter := make(chan error, 1)
	go func() {
		_, err := c.get(ctx, "k", func(context.Context) ([][]float32, error) {
			t.Error("waiter ran its own load while the leader was loading")
			return nil, nil
		})
		waiter <- err
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case err := <-waiter:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("waiter err = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancelled waiter still blocked on the leader")
	}
	close(release)
	if err := <-leader; err != nil {
		t.Fatal(err)
	}
}

func TestCatalogVectorCacheWaiterRetriesAfterLeaderFails(t *testing.T) {
	c := &catalogVectorCache{entries: map[string]*catalogVectorEntry{}}
	release := make(chan struct{})
	started := make(chan struct{})
	leader := make(chan error, 1)
	go func() {
		_, err := c.get(context.Background(), "k", func(context.Context) ([][]float32, error) {
			close(started)
			<-release
			return nil, errors.New("boom")
		})
		leader <- err
	}()
	<-started

	type result struct {
		vectors [][]float32
		err     error
	}
	waiter := make(chan result, 1)
	go func() {
		v, err := c.get(context.Background(), "k", func(context.Context) ([][]float32, error) {
			return [][]float32{{7}}, nil
		})
		waiter <- result{v, err}
	}()
	time.Sleep(20 * time.Millisecond)
	close(release)
	if err := <-leader; err == nil {
		t.Fatal("leader should fail")
	}
	got := <-waiter
	if got.err != nil || len(got.vectors) != 1 || got.vectors[0][0] != 7 {
		t.Fatalf("waiter = %+v, want its own fresh load", got)
	}
}

func TestCatalogVectorCacheSkipsLoadForExpiredContext(t *testing.T) {
	c := &catalogVectorCache{entries: map[string]*catalogVectorEntry{}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := c.get(ctx, "k", func(context.Context) ([][]float32, error) {
		t.Error("load ran with a dead context")
		return nil, nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestCatalogVectorCacheExceedsCapWhileEverySlotLoads(t *testing.T) {
	c := &catalogVectorCache{entries: map[string]*catalogVectorEntry{}}
	release := make(chan struct{})
	var started sync.WaitGroup
	slow := func(context.Context) ([][]float32, error) {
		started.Done()
		<-release
		return [][]float32{{1}}, nil
	}
	total := maxCatalogVectorEntries + 1
	started.Add(total)
	errs := make(chan error, total)
	for i := range total {
		go func() {
			_, err := c.get(context.Background(), "k"+strconv.Itoa(i), slow)
			errs <- err
		}()
	}
	started.Wait()
	c.mu.Lock()
	size := len(c.entries)
	c.mu.Unlock()
	if size != total {
		t.Fatalf("entries while loading = %d, want %d: loading entries must not be evicted", size, total)
	}
	close(release)
	for range total {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
}
