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
	"time"
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

// installRequest is what `models ensure --tts` asks for; a conversation start
// asks for AssetRequest{NeedTTS: true} alone.
var installRequest = AssetRequest{NeedTTS: true, NeedVoiceCatalog: true}

func installTTS(ctx context.Context, cfg *Config, log *tickLog) error {
	return EnsureRuntimeAssetsProgress(ctx, cfg, installRequest, log.add)
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
	if err := installTTS(context.Background(), cfg, log); err != nil {
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
	if err := installTTS(context.Background(), cfg, log); err != nil {
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
	if err := installTTS(context.Background(), cfg, log); err != nil {
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

		err := EnsureRuntimeAssetsProgress(ctx, cfg, installRequest, func(p AssetProgress) {
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

		err := EnsureRuntimeAssetsProgress(ctx, cfg, installRequest, func(p AssetProgress) {
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
		err := installTTS(context.Background(), openAISpeechCfg(t, base), &tickLog{})
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
		err := installTTS(context.Background(), cfg, &tickLog{})
		if err == nil || !strings.Contains(err.Error(), "file does not exist") {
			t.Fatalf("err = %v", err)
		}
		if *hits != 0 {
			t.Fatal("catalog fetched after a failed pull")
		}
	})
	t.Run("a stream that ends without success fails", func(t *testing.T) {
		fake := &fakeOllama{version: "0.40.9", tags: []string{}, pull: pullStream(`{"status":"pulling manifest"}`)}
		err := installTTS(context.Background(), openAISpeechCfg(t, fake.start(t).URL+"/v1"), &tickLog{})
		if err == nil || !strings.Contains(err.Error(), "before Ollama reported success") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("an Ollama too old for Phonon is not pulled into", func(t *testing.T) {
		fake := &fakeOllama{version: "0.40.8", tags: []string{}}
		err := installTTS(context.Background(), openAISpeechCfg(t, fake.start(t).URL+"/v1"), &tickLog{})
		if err == nil || !strings.Contains(err.Error(), MinPhononOllama) || len(fake.pullBodies()) != 0 {
			t.Fatalf("err = %v, pulls = %v", err, fake.pullBodies())
		}
	})
	t.Run("a catalog that cannot be fetched closes as skipped with the reason", func(t *testing.T) {
		fake := &fakeOllama{version: "0.40.9", tags: []string{"phonon:latest"}}
		cfg := openAISpeechCfg(t, fake.start(t).URL+"/v1")
		fakeCatalog(t, http.StatusInternalServerError, "")
		log := &tickLog{}
		if err := installTTS(context.Background(), cfg, log); err != nil {
			t.Fatalf("err = %v, want a warning only", err)
		}
		if got := log.phases(OpenAISpeechVoicesFile); !slices.Equal(got, []string{"start", "skipped"}) {
			t.Fatalf("catalog phases = %v, want the row closed", got)
		}
		if last := log.last(OpenAISpeechVoicesFile); !strings.Contains(last.Detail, "HTTP 500") {
			t.Fatalf("closing tick = %+v, want the reason", last)
		}
		assertNoCatalogLeft(t, cfg)
	})
	t.Run("a 401 from a proxy names the API key, not ollama serve", func(t *testing.T) {
		cfg := openAISpeechCfg(t, rejectingServer(t)+"/v1")
		err := installTTS(context.Background(), cfg, &tickLog{})
		if err == nil || !strings.Contains(err.Error(), "rejected the request (401): check openai_speech_api_key") || strings.Contains(err.Error(), "ollama serve") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("a pull that goes quiet says so instead of context canceled", func(t *testing.T) {
		orig := ollamaPullStallTimeout
		ollamaPullStallTimeout = 50 * time.Millisecond
		t.Cleanup(func() { ollamaPullStallTimeout = orig })
		fake := &fakeOllama{version: "0.40.9", tags: []string{}, pull: func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprintln(w, `{"status":"pulling sha256:aa","digest":"sha256:aa","total":100,"completed":10}`)
			w.(http.Flusher).Flush()
			<-r.Context().Done()
		}}
		err := installTTS(context.Background(), openAISpeechCfg(t, fake.start(t).URL+"/v1"), &tickLog{})
		if err == nil || errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), "pull phonon") || !strings.Contains(err.Error(), "no progress from Ollama for 0.05s") {
			t.Fatalf("err = %v", err)
		}
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

func TestOpenAISpeechSendsTheAPIKeyToOllama(t *testing.T) {
	fake := &fakeOllama{version: "0.40.9", tags: []string{}, voices: []string{"Zoey"}}
	cfg := openAISpeechCfg(t, fake.start(t).URL+"/v1")
	cfg.OpenAISpeechAPIKey = "sk-test"
	fakeCatalog(t, http.StatusOK, fakeVoicesJSONL)

	if err := installTTS(context.Background(), cfg, &tickLog{}); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	fake.tags = []string{"phonon:latest"}
	DiagnoseOpenAISpeech(context.Background(), cfg, ModelsDirFrom(cfg))
	for _, path := range []string{"/api/version", "/api/tags", "/api/pull", "/api/show"} {
		if got := fake.authOn(path); got != "Bearer sk-test" {
			t.Errorf("%s Authorization = %q", path, got)
		}
	}
}

func TestConversationStartNeverFetchesTheCatalog(t *testing.T) {
	fake := &fakeOllama{version: "0.40.9", tags: []string{}}
	cfg := openAISpeechCfg(t, fake.start(t).URL+"/v1")
	_, hits := fakeCatalog(t, http.StatusOK, fakeVoicesJSONL)

	log := &tickLog{}
	if err := EnsureRuntimeAssetsProgress(context.Background(), cfg, AssetRequest{NeedTTS: true}, log.add); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if *hits != 0 {
		t.Fatalf("a conversation start asked Hugging Face %d times for voices.jsonl", *hits)
	}
	if got := log.phases(OpenAISpeechVoicesFile); got != nil {
		t.Fatalf("catalog ticks at a conversation start: %v", got)
	}
	if n := len(fake.pullBodies()); n != 1 {
		t.Fatalf("pulls = %d, want the missing model pulled as Kokoro and Qwen are", n)
	}
}

func TestTheCatalogMustBeACatalog(t *testing.T) {
	fake := &fakeOllama{version: "0.40.9", tags: []string{"phonon:latest"}}
	t.Run("a captive portal page is refused and names the URL", func(t *testing.T) {
		cfg := openAISpeechCfg(t, fake.start(t).URL+"/v1")
		srv, _ := fakeCatalog(t, http.StatusOK, "<!doctype html><html><body>Sign in to the Wi-Fi</body></html>\n")
		log := &tickLog{}
		if err := installTTS(context.Background(), cfg, log); err != nil {
			t.Fatalf("err = %v", err)
		}
		if last := log.last(OpenAISpeechVoicesFile); last.Phase != "skipped" || !strings.Contains(last.Detail, srv.URL+"/voices.jsonl") {
			t.Fatalf("closing tick = %+v, want a refusal naming the URL", last)
		}
		assertNoCatalogLeft(t, cfg)
	})
	t.Run("an empty body is refused", func(t *testing.T) {
		cfg := openAISpeechCfg(t, fake.start(t).URL+"/v1")
		fakeCatalog(t, http.StatusOK, "")
		if err := installTTS(context.Background(), cfg, &tickLog{}); err != nil {
			t.Fatalf("err = %v", err)
		}
		assertNoCatalogLeft(t, cfg)
	})
	t.Run("a 0-byte file on disk is not installed and is replaced", func(t *testing.T) {
		cfg := openAISpeechCfg(t, fake.start(t).URL+"/v1")
		_, hits := fakeCatalog(t, http.StatusOK, fakeVoicesJSONL)
		if err := os.MkdirAll(filepath.Dir(voicesPath(cfg)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(voicesPath(cfg), nil, 0o644); err != nil {
			t.Fatal(err)
		}
		if row := statusRows(t, cfg)[OpenAISpeechVoicesAssetID]; row.Installed || !strings.Contains(row.Detail, "not a voice catalog") {
			t.Fatalf("status of an empty file = %+v", row)
		}
		if diag := diagByName(DiagnoseOpenAISpeech(context.Background(), cfg, ModelsDirFrom(cfg)))["openai-speech-catalog-file"]; diag.Severity != SeverityWarn {
			t.Fatalf("doctor on an empty file = %+v", diag)
		}
		if err := installTTS(context.Background(), cfg, &tickLog{}); err != nil {
			t.Fatal(err)
		}
		if data, err := os.ReadFile(voicesPath(cfg)); *hits != 1 || err != nil || string(data) != fakeVoicesJSONL {
			t.Fatalf("after ensure: hits %d, %q, %v", *hits, data, err)
		}
	})
}

func TestPullProgressNeverRunsBackwards(t *testing.T) {
	fake := &fakeOllama{version: "0.40.9", pull: pullStream(
		`{"status":"pulling sha256:aa","digest":"sha256:aa","total":100,"completed":80}`,
		`{"status":"pulling sha256:bb","digest":"sha256:bb","total":100,"completed":0}`,
		`{"status":"pulling sha256:bb","digest":"sha256:bb","total":100,"completed":100}`,
		`{"status":"pulling sha256:aa","digest":"sha256:aa","total":100,"completed":100}`,
		`{"status":"success"}`,
	)}
	srv := fake.start(t)
	target := OpenAISpeechTarget{Base: srv.URL + "/v1", Root: srv.URL, Model: "phonon", Ollama: true, Version: "0.40.9", Reachable: true, ModelsKnown: true}
	log := &tickLog{}
	if err := ensureOllamaModel(context.Background(), target, "", log.add); err != nil {
		t.Fatal(err)
	}
	var pcts []float64
	for _, p := range log.ticks {
		pcts = append(pcts, p.Pct)
	}
	for i := 1; i < len(pcts); i++ {
		if pcts[i] < pcts[i-1] {
			t.Fatalf("pct went backwards: %v", pcts)
		}
	}
	if len(pcts) < 4 || pcts[2] != 80 {
		t.Fatalf("pcts = %v, want the late layer held at 80 rather than dropping to 40", pcts)
	}
}

func TestAFailedListingIsNotAMissingModel(t *testing.T) {
	// Ollama answers /api/version, but /api/tags 404s and /v1/models 500s:
	// nothing proved phonon is absent, so nothing may be pulled.
	fake := &fakeOllama{version: "0.40.9", modelsStatus: http.StatusInternalServerError}
	cfg := openAISpeechCfg(t, fake.start(t).URL+"/v1")
	_, hits := fakeCatalog(t, http.StatusOK, fakeVoicesJSONL)

	err := installTTS(context.Background(), cfg, &tickLog{})
	if err == nil || !strings.Contains(err.Error(), "could not check whether phonon is installed") || !strings.Contains(err.Error(), "/api/tags returned 404") || !strings.Contains(err.Error(), "/v1/models returned 500") {
		t.Fatalf("err = %v", err)
	}
	if n := len(fake.pullBodies()); n != 0 {
		t.Fatalf("pulled %d times on a listing that failed", n)
	}
	if *hits != 0 {
		t.Fatal("fetched the catalog after the model check failed")
	}

	model := statusRows(t, cfg)[OpenAISpeechModelAssetID]
	if model.Installed || !strings.HasPrefix(model.Detail, "unknown: could not check") || strings.Contains(model.Detail, "not pulled") {
		t.Fatalf("model row = %+v, want unknown rather than missing", model)
	}
	got := diagByName(DiagnoseOpenAISpeech(context.Background(), cfg, ModelsDirFrom(cfg)))
	if row := got["openai-speech-model"]; row.Severity != SeverityWarn || !strings.Contains(row.Detail, "could not check") {
		t.Fatalf("doctor model row = %+v", row)
	}
	if _, ok := got["openai-speech-voices"]; ok {
		t.Fatal("asked for the voices of a model nobody could find")
	}
}
