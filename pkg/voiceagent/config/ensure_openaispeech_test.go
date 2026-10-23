package config

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
)

const fakeVoicesJSONL = `{"name":"Zoey","locale":"en-us","gender":"female"}` + "\n"

type tickLog struct {
	mu    sync.Mutex
	ticks []AssetProgress
}

func (l *tickLog) add(p AssetProgress) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.ticks = append(l.ticks, p)
}

// phases is the asset's phase sequence with repeats collapsed.
func (l *tickLog) phases(asset string) []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []string
	for _, p := range l.ticks {
		if p.Asset == asset && (len(out) == 0 || out[len(out)-1] != p.Phase) {
			out = append(out, p.Phase)
		}
	}
	return out
}

func (l *tickLog) last(asset string) AssetProgress {
	l.mu.Lock()
	defer l.mu.Unlock()
	var last AssetProgress
	for _, p := range l.ticks {
		if p.Asset == asset {
			last = p
		}
	}
	return last
}

func ensureTTS(ctx context.Context, cfg *Config, log *tickLog) error {
	return EnsureRuntimeAssetsProgress(ctx, cfg, AssetRequest{NeedTTS: true}, log.add)
}

func pullStream(lines ...string) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, _ *http.Request) {
		for _, l := range lines {
			fmt.Fprintln(w, l)
			w.(http.Flusher).Flush()
		}
	}
}

func TestEnsureOpenAISpeechPullsAMissingModelThenTheCatalog(t *testing.T) {
	fake := &fakeOllama{version: "0.40.9", tags: []string{}, pull: pullStream(
		`{"status":"pulling manifest"}`,
		`{"status":"pulling sha256:aa","digest":"sha256:aa","total":100,"completed":0}`,
		`{"status":"pulling sha256:aa","digest":"sha256:aa","total":100,"completed":60}`,
		`{"status":"pulling sha256:aa","digest":"sha256:aa","total":100,"completed":100}`,
		`{"status":"pulling sha256:bb","digest":"sha256:bb","total":10,"completed":10}`,
		`{"status":"verifying sha256 digest"}`,
		`{"status":"writing manifest"}`,
		`{"status":"success"}`,
	)}
	cfg := openAISpeechCfg(t, fake.start(t).URL+"/v1")
	fakeCatalog(t, http.StatusOK, fakeVoicesJSONL)

	log := &tickLog{}
	if err := ensureTTS(context.Background(), cfg, log); err != nil {
		t.Fatalf("ensure: %v", err)
	}

	pulls := fake.pullBodies()
	if len(pulls) != 1 || pulls[0]["model"] != "phonon" || pulls[0]["stream"] != true {
		t.Fatalf("pull requests = %v, want one {model: phonon, stream: true}", pulls)
	}
	model := OpenAISpeechModelAssetName("phonon")
	if got, want := log.phases(model), []string{"start", "download", "verify", "install", "done"}; !slices.Equal(got, want) {
		t.Fatalf("model phases = %v, want %v", got, want)
	}
	if done := log.last(model); done.Bytes != 110 || done.Total != 110 || done.Pct != 100 {
		t.Fatalf("model done tick = %+v, want both layers summed", done)
	}
	if got, want := log.phases(OpenAISpeechVoicesFile), []string{"start", "download", "verify", "done"}; !slices.Equal(got, want) {
		t.Fatalf("catalog phases = %v, want %v", got, want)
	}
	data, err := os.ReadFile(voicesPath(cfg))
	if err != nil || string(data) != fakeVoicesJSONL {
		t.Fatalf("voices.jsonl = %q, %v", data, err)
	}
}

func TestEnsureOpenAISpeechSkipsWhatIsThere(t *testing.T) {
	fake := &fakeOllama{version: "0.40.9", tags: []string{"phonon:latest"}}
	cfg := openAISpeechCfg(t, fake.start(t).URL+"/v1")
	_, hits := fakeCatalog(t, http.StatusOK, fakeVoicesJSONL)
	writeVoicesFile(t, cfg)

	log := &tickLog{}
	if err := ensureTTS(context.Background(), cfg, log); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if n := len(fake.pullBodies()); n != 0 {
		t.Fatalf("pulled %d times with the model already listed", n)
	}
	if *hits != 0 {
		t.Fatalf("fetched the catalog %d times with it on disk", *hits)
	}
	for _, asset := range []string{OpenAISpeechModelAssetName("phonon"), OpenAISpeechVoicesFile} {
		if got := log.phases(asset); !slices.Equal(got, []string{"skipped"}) {
			t.Fatalf("%s phases = %v, want [skipped]", asset, got)
		}
	}
}

func TestEnsureOpenAISpeechOnAnotherServerOnlyFetchesTheCatalog(t *testing.T) {
	ptts := &fakeOllama{tags: []string{"phonon"}, notOllama: true}
	cfg := openAISpeechCfg(t, ptts.start(t).URL+"/v1")
	fakeCatalog(t, http.StatusOK, fakeVoicesJSONL)

	log := &tickLog{}
	if err := ensureTTS(context.Background(), cfg, log); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if n := len(ptts.pullBodies()); n != 0 {
		t.Fatalf("pulled from a non-Ollama server %d times", n)
	}
	if got := log.phases(OpenAISpeechModelAssetName("phonon")); got != nil {
		t.Fatalf("model ticks for a non-Ollama server: %v", got)
	}
	if _, err := os.Stat(voicesPath(cfg)); err != nil {
		t.Fatalf("catalog not fetched: %v", err)
	}
}

func TestEnsureOpenAISpeechCancellationLeavesNoPartialCatalog(t *testing.T) {
	t.Run("mid pull", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		fake := &fakeOllama{version: "0.40.9", tags: []string{}, pull: func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprintln(w, `{"status":"pulling sha256:aa","digest":"sha256:aa","total":100,"completed":10}`)
			w.(http.Flusher).Flush()
			<-r.Context().Done()
		}}
		cfg := openAISpeechCfg(t, fake.start(t).URL+"/v1")
		_, hits := fakeCatalog(t, http.StatusOK, fakeVoicesJSONL)

		err := EnsureRuntimeAssetsProgress(ctx, cfg, AssetRequest{NeedTTS: true}, func(p AssetProgress) {
			if p.Phase == assetPhaseDownload {
				cancel()
			}
		})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
		if *hits != 0 {
			t.Fatal("catalog fetched after the pull was cancelled")
		}
		assertNoCatalogLeft(t, cfg)
	})
	t.Run("mid catalog download", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		fake := &fakeOllama{version: "0.40.9", tags: []string{"phonon:latest"}}
		cfg := openAISpeechCfg(t, fake.start(t).URL+"/v1")
		slow := newBlockingCatalog(t)

		err := EnsureRuntimeAssetsProgress(ctx, cfg, AssetRequest{NeedTTS: true}, func(p AssetProgress) {
			if p.Asset == OpenAISpeechVoicesFile && p.Phase == assetPhaseDownload {
				cancel()
			}
		})
		close(slow)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
		assertNoCatalogLeft(t, cfg)
	})
}

// newBlockingCatalog serves half a catalog and then waits until the returned
// channel closes.
func newBlockingCatalog(t *testing.T) chan struct{} {
	t.Helper()
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "1000")
		fmt.Fprint(w, `{"name":"Zo`)
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(srv.Close)
	fakeVoicesURL(t, srv.URL+"/voices.jsonl")
	return release
}

func assertNoCatalogLeft(t *testing.T, cfg *Config) {
	t.Helper()
	dir := filepath.Dir(voicesPath(cfg))
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("left behind in %s: %v", dir, names)
	}
}

func TestEnsureOpenAISpeechFailures(t *testing.T) {
	t.Run("server down names the URL and ollama serve", func(t *testing.T) {
		base := closedURL(t) + "/v1"
		err := ensureTTS(context.Background(), openAISpeechCfg(t, base), &tickLog{})
		if err == nil || !strings.Contains(err.Error(), base) || !strings.Contains(err.Error(), "ollama serve") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("an error line fails the pull", func(t *testing.T) {
		fake := &fakeOllama{version: "0.40.9", tags: []string{}, pull: pullStream(
			`{"status":"pulling manifest"}`,
			`{"error":"pull model manifest: file does not exist"}`,
		)}
		cfg := openAISpeechCfg(t, fake.start(t).URL+"/v1")
		_, hits := fakeCatalog(t, http.StatusOK, fakeVoicesJSONL)
		err := ensureTTS(context.Background(), cfg, &tickLog{})
		if err == nil || !strings.Contains(err.Error(), "file does not exist") {
			t.Fatalf("err = %v", err)
		}
		if *hits != 0 {
			t.Fatal("catalog fetched after a failed pull")
		}
	})
	t.Run("a stream that ends without success fails", func(t *testing.T) {
		fake := &fakeOllama{version: "0.40.9", tags: []string{}, pull: pullStream(`{"status":"pulling manifest"}`)}
		err := ensureTTS(context.Background(), openAISpeechCfg(t, fake.start(t).URL+"/v1"), &tickLog{})
		if err == nil || !strings.Contains(err.Error(), "before Ollama reported success") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("an Ollama too old for Phonon is not pulled into", func(t *testing.T) {
		fake := &fakeOllama{version: "0.40.8", tags: []string{}}
		err := ensureTTS(context.Background(), openAISpeechCfg(t, fake.start(t).URL+"/v1"), &tickLog{})
		if err == nil || !strings.Contains(err.Error(), MinPhononOllama) || len(fake.pullBodies()) != 0 {
			t.Fatalf("err = %v, pulls = %v", err, fake.pullBodies())
		}
	})
	t.Run("a catalog that cannot be fetched does not fail ensure", func(t *testing.T) {
		fake := &fakeOllama{version: "0.40.9", tags: []string{"phonon:latest"}}
		cfg := openAISpeechCfg(t, fake.start(t).URL+"/v1")
		fakeCatalog(t, http.StatusInternalServerError, "")
		if err := ensureTTS(context.Background(), cfg, &tickLog{}); err != nil {
			t.Fatalf("err = %v, want a warning only", err)
		}
		assertNoCatalogLeft(t, cfg)
	})
}

func TestEnsureRuntimeAssetsLeavesOpenAISpeechAloneWithoutTTS(t *testing.T) {
	fake := &fakeOllama{version: "0.40.9", tags: []string{}}
	cfg := openAISpeechCfg(t, fake.start(t).URL+"/v1")
	if err := EnsureRuntimeAssetsProgress(context.Background(), cfg, AssetRequest{}, nil); err != nil {
		t.Fatal(err)
	}
	if n := len(fake.pullBodies()); n != 0 {
		t.Fatalf("pulled %d times with no TTS requested", n)
	}
}
