package imagegen

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// fakeComfy is a ComfyUI server that queues one graph and finishes it after
// a number of history reads, or never when finishAfter is negative.
type fakeComfy struct {
	t           *testing.T
	finishAfter int
	failWith    string
	refuse      string
	// vanish leaves the graph out of the queue as well as the history.
	vanish bool

	mu       sync.Mutex
	graph    map[string]any
	reads    int
	deleted  []string
	cancels  []string
	forgot   []string
	readFile string
}

func (f *fakeComfy) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /prompt", func(w http.ResponseWriter, r *http.Request) {
		if f.refuse != "" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, f.refuse)
			return
		}
		var body struct {
			Prompt map[string]any `json:"prompt"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			f.t.Errorf("decode /prompt: %v", err)
		}
		f.mu.Lock()
		f.graph = body.Prompt
		f.mu.Unlock()
		_, _ = io.WriteString(w, `{"prompt_id":"job-1","number":1,"node_errors":{}}`)
	})
	mux.HandleFunc("GET /history/{id}", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.reads++
		reads := f.reads
		f.mu.Unlock()
		switch {
		case f.finishAfter < 0 || reads <= f.finishAfter:
			_, _ = io.WriteString(w, `{}`)
		case f.failWith != "":
			_, _ = io.WriteString(w, `{"job-1":{"status":{"status_str":"error","completed":false,"messages":[["execution_start",{}],["execution_error",{"node_type":"KSampler","exception_message":"`+f.failWith+`"}]]},"outputs":{}}}`)
		default:
			_, _ = io.WriteString(w, `{"job-1":{"status":{"status_str":"success","completed":true,"messages":[]},"outputs":{"9":{"images":[{"filename":"out_00001_.png","subfolder":"","type":"temp"}]}}}}`)
		}
	})
	mux.HandleFunc("GET /view", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.readFile = r.URL.Query().Get("filename") + "|" + r.URL.Query().Get("type")
		f.mu.Unlock()
		_, _ = w.Write(tinyPNG(f.t))
	})
	mux.HandleFunc("GET /queue", func(w http.ResponseWriter, r *http.Request) {
		if f.vanish {
			_, _ = io.WriteString(w, `{"queue_running":[],"queue_pending":[[2,"other-job",{}]]}`)
			return
		}
		_, _ = io.WriteString(w, `{"queue_running":[[1,"job-1",{}]],"queue_pending":[]}`)
	})
	mux.HandleFunc("POST /queue", func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Delete []string }
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		f.deleted = append(f.deleted, body.Delete...)
		f.mu.Unlock()
	})
	mux.HandleFunc("POST /interrupt", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			PromptID string `json:"prompt_id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		f.cancels = append(f.cancels, body.PromptID)
		f.mu.Unlock()
	})
	mux.HandleFunc("POST /history", func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Delete []string }
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		f.forgot = append(f.forgot, body.Delete...)
		f.mu.Unlock()
	})
	return mux
}

func tinyPNG(t *testing.T) []byte {
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func serve(t *testing.T, f *fakeComfy) *ComfyUI {
	t.Helper()
	f.t = t
	srv := httptest.NewServer(f.handler())
	t.Cleanup(srv.Close)
	c, err := NewComfyUI(srv.URL, "")
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestGenerateFillsTheGraphAndReturnsThePicture(t *testing.T) {
	f := &fakeComfy{finishAfter: 1}
	c := serve(t, f)
	got, err := c.Generate(t.Context(), Request{Prompt: "a lighthouse at dusk", Width: 832, Height: 1216})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, tinyPNG(t)) {
		t.Fatal("returned bytes are not the server's picture")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	text := f.graph["5"].(map[string]any)["inputs"].(map[string]any)["text"]
	latent := f.graph["7"].(map[string]any)["inputs"].(map[string]any)
	seed := f.graph["8"].(map[string]any)["inputs"].(map[string]any)["seed"]
	if text != "a lighthouse at dusk" || latent["width"] != float64(832) || latent["height"] != float64(1216) {
		t.Fatalf("graph not filled: text=%v latent=%v", text, latent)
	}
	if _, ok := seed.(float64); !ok {
		t.Fatalf("seed = %#v, want a number", seed)
	}
	if f.readFile != "out_00001_.png|temp" {
		t.Fatalf("read %q", f.readFile)
	}
	if len(f.forgot) != 1 || f.forgot[0] != "job-1" || len(f.cancels) != 0 || len(f.deleted) != 0 {
		t.Fatalf("forgot=%v cancels=%v deleted=%v", f.forgot, f.cancels, f.deleted)
	}
}

func TestGenerateUsesAFreshSeedEachTime(t *testing.T) {
	f := &fakeComfy{finishAfter: 0}
	c := serve(t, f)
	seeds := map[float64]bool{}
	for range 3 {
		if _, err := c.Generate(t.Context(), Request{Prompt: "x", Width: 8, Height: 8}); err != nil {
			t.Fatal(err)
		}
		f.mu.Lock()
		seeds[f.graph["8"].(map[string]any)["inputs"].(map[string]any)["seed"].(float64)] = true
		f.mu.Unlock()
	}
	if len(seeds) != 3 {
		t.Fatalf("seeds repeated: %v", seeds)
	}
}

func TestGenerateNamesAMissingModelWithoutTheInstalledList(t *testing.T) {
	f := &fakeComfy{refuse: `{"error":{"type":"prompt_outputs_failed_validation","message":"Prompt outputs failed validation"},"node_errors":{"1":{"class_type":"UNETLoader","errors":[{"type":"value_not_in_list","message":"Value not in list","details":"unet_name: 'fake_model.safetensors' not in ['other_a.safetensors', 'other_b.safetensors']"}]}}}`}
	c := serve(t, f)
	_, err := c.Generate(t.Context(), Request{Prompt: "x", Width: 8, Height: 8})
	if err == nil || !strings.Contains(err.Error(), "'fake_model.safetensors' is not installed") || strings.Contains(err.Error(), "other_a") {
		t.Fatalf("err = %v", err)
	}
}

func TestGenerateReportsAnExecutionError(t *testing.T) {
	f := &fakeComfy{finishAfter: 0, failWith: "out of memory"}
	c := serve(t, f)
	_, err := c.Generate(t.Context(), Request{Prompt: "x", Width: 8, Height: 8})
	if err == nil || !strings.Contains(err.Error(), "KSampler: out of memory") {
		t.Fatalf("err = %v", err)
	}
}

func TestCancelledGenerationStopsOnlyItsOwnGraph(t *testing.T) {
	f := &fakeComfy{finishAfter: -1}
	c := serve(t, f)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		_, err := c.Generate(ctx, Request{Prompt: "x", Width: 8, Height: 8})
		done <- err
	}()
	for {
		f.mu.Lock()
		reads := f.reads
		f.mu.Unlock()
		if reads > 0 {
			break
		}
	}
	cancel()
	if err := <-done; err != context.Canceled {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.deleted) != 1 || f.deleted[0] != "job-1" || len(f.cancels) != 1 || f.cancels[0] != "job-1" {
		t.Fatalf("deleted=%v cancels=%v, want only job-1", f.deleted, f.cancels)
	}
}

func TestGenerateSaysWhenComfyUIIsNotRunning(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	addr := srv.URL
	srv.Close()
	c, err := NewComfyUI(addr, "")
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Generate(t.Context(), Request{Prompt: "x", Width: 8, Height: 8})
	if err == nil || !strings.Contains(err.Error(), "is not answering") {
		t.Fatalf("err = %v", err)
	}
}

func TestWorkflowFile(t *testing.T) {
	dir := t.TempDir()
	without := filepath.Join(dir, "without.json")
	if err := os.WriteFile(without, []byte(`{"1":{"class_type":"CLIPTextEncode","inputs":{"text":"fixed"}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	c, _ := NewComfyUI("http://127.0.0.1:1", without)
	if _, err := c.Generate(t.Context(), Request{Prompt: "x"}); err == nil || !strings.Contains(err.Error(), "{{prompt}}") {
		t.Fatalf("err = %v", err)
	}

	// A graph exported with its {"prompt": ...} wrapper is accepted.
	wrapped := filepath.Join(dir, "wrapped.json")
	if err := os.WriteFile(wrapped, []byte(`{"prompt":{"1":{"class_type":"CLIPTextEncode","inputs":{"text":"style, {{prompt}}"}},"2":{"class_type":"PreviewImage","inputs":{"images":["1",0]}}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	f := &fakeComfy{finishAfter: 0}
	f.t = t
	srv := httptest.NewServer(f.handler())
	defer srv.Close()
	c, _ = NewComfyUI(srv.URL, wrapped)
	if _, err := c.Generate(t.Context(), Request{Prompt: "a cat"}); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	got := f.graph["1"].(map[string]any)["inputs"].(map[string]any)["text"]
	f.mu.Unlock()
	if got != "style, a cat" {
		t.Fatalf("text = %v", got)
	}
}

func TestGenerateStopsWaitingWhenTheGraphIsDropped(t *testing.T) {
	f := &fakeComfy{finishAfter: -1, vanish: true}
	c := serve(t, f)
	_, err := c.Generate(t.Context(), Request{Prompt: "x", Width: 8, Height: 8})
	if err == nil || !strings.Contains(err.Error(), "dropped the image workflow") {
		t.Fatalf("err = %v", err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.reads != 2 {
		t.Fatalf("history read %d times, want 2", f.reads)
	}
}

func TestNewComfyUIRejectsANonHTTPAddress(t *testing.T) {
	for _, bad := range []string{"", "localhost:1234", "ftp://host"} {
		if _, err := NewComfyUI(bad, ""); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}
