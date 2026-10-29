package brain

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ollama/ollama/api"
)

func TestValidateLegacyWorkerRejectsRemoteMetadata(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"details":{"family":"qwen"},"remote_host":"https://ollama.com","remote_model":"m"}`)
	}))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	o := &OllamaBrain{client: api.NewClient(u, http.DefaultClient), model: "m"}
	if err := o.ValidateLegacyWorker(context.Background()); err == nil {
		t.Fatal("remote model accepted")
	}
}

func TestValidateLegacyWorkerAcceptsLocalMetadata(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"details":{"family":"qwen"},"capabilities":["completion","tools"]}`)
	}))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	o := &OllamaBrain{client: api.NewClient(u, http.DefaultClient), model: "m"}
	if err := o.ValidateLegacyWorker(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestOllamaWorkerRequiresToolsEvenWithoutTheWorkInbox(t *testing.T) {
	for _, caps := range []string{`["completion"]`, `["embedding"]`, `[]`, `null`, `["completion","tools"]`} {
		t.Run(caps, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/show" {
					t.Errorf("validation invoked inference: %s", r.URL.Path)
				}
				fmt.Fprintf(w, `{"details":{"family":"fixture"},"capabilities":%s}`, caps)
			}))
			defer srv.Close()
			u, _ := url.Parse(srv.URL)
			o := &OllamaBrain{client: api.NewClient(u, http.DefaultClient), model: "fixture"}
			wantOK := caps == `["completion","tools"]`
			if err := o.ValidateWorkerTools(t.Context()); (err == nil) != wantOK {
				t.Fatalf("capabilities %s: %v", caps, err)
			}
			if err := o.ValidateLegacyWorker(t.Context()); (err == nil) != wantOK {
				t.Fatalf("legacy capabilities %s: %v", caps, err)
			}
		})
	}
}

func TestValidateLegacyWorkerCloudAliasRejectedBeforeHTTP(t *testing.T) {
	var called atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called.Store(true) }))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	o := &OllamaBrain{client: api.NewClient(u, http.DefaultClient), model: "innocent:cloud-remote"}
	if err := o.ValidateLegacyWorker(context.Background()); err == nil {
		t.Fatal("cloud alias accepted")
	}
	if called.Load() {
		t.Fatal("cloud alias performed metadata request")
	}
}

func TestValidateLegacyWorkerCancellationStopsHangingShow(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		close(started)
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer func() { close(release); srv.Close() }()
	u, _ := url.Parse(srv.URL)
	o := &OllamaBrain{client: api.NewClient(u, http.DefaultClient), model: "m"}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- o.ValidateLegacyWorker(ctx) }()
	<-started
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("validation did not stop after cancellation")
	}
}
