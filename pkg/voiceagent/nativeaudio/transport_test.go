package nativeaudio

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestNativeHTTPLeaseCoversStreamingBody(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := requests.Add(1)
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		if n == 1 {
			<-r.Context().Done()
			return
		}
		_, _ = io.WriteString(w, "done")
	}))
	defer server.Close()
	client, err := NewClient(server.URL, "chosen-model")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if err := client.BindScheduler(NewScheduler()); err != nil {
		t.Fatal(err)
	}
	first, err := client.http.Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	if response, err := client.http.Do(request); !errors.Is(err, context.DeadlineExceeded) {
		if response != nil {
			_ = response.Body.Close()
		}
		t.Fatal("second producer bypassed open body", err)
	}
	cancel()
	if requests.Load() != 1 {
		t.Fatal("overlapping HTTP producer")
	}
	if err := first.Body.Close(); err != nil {
		t.Fatal(err)
	}
	third, err := client.http.Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	_ = third.Body.Close()
	if requests.Load() != 2 {
		t.Fatal("lease did not release")
	}
}
func TestSchedulerOwnedHTTPAvoidsNestedAcquisitionAndRejectsLateDispatch(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1); _, _ = io.WriteString(w, "done") }))
	defer server.Close()
	client, err := NewClient(server.URL, "chosen-model")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	scheduler := NewScheduler()
	if err := client.BindScheduler(scheduler); err != nil {
		t.Fatal(err)
	}
	var retired context.Context
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	err = scheduler.Run(ctx, func(owned context.Context) error {
		retired = owned
		request, err := http.NewRequestWithContext(owned, http.MethodGet, server.URL, nil)
		if err != nil {
			return err
		}
		response, err := client.http.Do(request)
		if err != nil {
			return err
		}
		return response.Body.Close()
	})
	if err != nil {
		t.Fatal(err)
	}
	late, err := http.NewRequestWithContext(retired, http.MethodGet, server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	if response, err := client.http.Do(late); err == nil {
		_ = response.Body.Close()
		t.Fatal("retired producer dispatched")
	}
	if requests.Load() != 1 {
		t.Fatal("late HTTP request reached server")
	}
}

func TestSharedClientRejectsWrongOriginAndInheritedAuthentication(t *testing.T) {
	var approved, other atomic.Int32
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { approved.Add(1) }))
	defer primary.Close()
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { other.Add(1) }))
	defer remote.Close()
	client, err := NewClient(primary.URL+"/owned", "chosen-model")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	for _, target := range []string{remote.URL + "/owned/api/chat", primary.URL + "/outside/api/chat"} {
		response, err := client.http.Get(target)
		if err == nil {
			_ = response.Body.Close()
			t.Fatal("accepted wrong endpoint", target)
		}
	}
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, primary.URL+"/owned/api/chat", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer unrelated")
	if response, err := client.http.Do(request); err == nil {
		_ = response.Body.Close()
		t.Fatal("borrowed unrelated authentication")
	}
	if approved.Load() != 0 || other.Load() != 0 {
		t.Fatal("rejected request reached a server")
	}
}

func TestRetiringClientCancelsAndJoinsActiveHTTPBody(t *testing.T) {
	started, stopped := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		close(started)
		<-r.Context().Done()
		close(stopped)
	}))
	defer server.Close()
	client, err := NewClient(server.URL, "chosen-model")
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.http.Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	<-started
	client.Close()
	if _, err := io.ReadAll(response.Body); err == nil {
		t.Fatal("retired body remained readable")
	}
	_ = response.Body.Close()
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("retired HTTP producer still running")
	}
	if next, err := client.http.Get(server.URL); err == nil {
		_ = next.Body.Close()
		t.Fatal("retired client dispatched")
	}
}
