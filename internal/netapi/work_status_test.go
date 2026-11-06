package netapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/work"
)

func TestCurrentWorkStatusIsAuthenticatedAndDoesNotExposeConfigurationOrOutput(t *testing.T) {
	scope := work.WorkScope{SessionID: "session", RuntimeEpoch: "runtime", ConversationEpoch: "epoch"}
	coordinator := work.NewCoordinator(scope)
	key, err := coordinator.Register(scope, work.Admission{Task: "inspect files", Provider: "ollama", Model: "model", Config: map[string]string{"secret": "private-config"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := coordinator.Publish(key, work.Outcome{Scope: scope, JobID: key.JobID, Status: work.OutcomeFailed, Text: "private-result", SourceRef: "private-path"}); err != nil {
		t.Fatal(err)
	}
	creds, err := LoadOrCreateCredentials(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	bus := events.NewBus()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dispatcher := NewDispatcher(&scriptedRunner{}, bus, nil, nil)
	go dispatcher.Run(ctx)
	server := New(Options{Bind: "127.0.0.1:0", Credentials: creds, Bus: bus, Dispatcher: dispatcher,
		CurrentWorkSnapshot: func() work.Snapshot { return coordinator.Snapshot(scope) }})
	done := make(chan error, 1)
	go func() { done <- server.ListenAndServe(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(3 * time.Second):
			t.Error("server did not stop")
		}
	})
	deadline := time.Now().Add(3 * time.Second)
	for server.Addr() == nil && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if server.Addr() == nil {
		t.Fatal("server did not start")
	}
	url := "https://" + server.Addr().String() + "/v1/work"
	client := insecureClient()
	defer client.CloseIdleConnections()
	response, err := client.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status = %d", response.StatusCode)
	}
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	req.Header.Set("Authorization", "Bearer "+creds.Token)
	response, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(response.Body)
	if err != nil || response.StatusCode != http.StatusOK {
		t.Fatalf("status=%d body=%s err=%v", response.StatusCode, raw, err)
	}
	for _, secret := range []string{"private-config", "private-result", "private-path"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("work status leaked %q", secret)
		}
	}
	var body struct {
		Work WorkStatus `json:"work"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	if body.Work.Scope != scope || len(body.Work.Jobs) != 1 || body.Work.Jobs[0].Task != "inspect files" || body.Work.Jobs[0].Outcome == nil || body.Work.Jobs[0].Outcome.Status != work.OutcomeFailed {
		t.Fatalf("work status = %+v", body.Work)
	}
}

func TestCurrentWorkStatusWithoutCoordinator(t *testing.T) {
	server := New(Options{CurrentWorkSnapshot: func() work.Snapshot { return work.Snapshot{} }})
	recorder := httptest.NewRecorder()
	server.handleCurrentWork(recorder, httptest.NewRequest(http.MethodGet, "/v1/work", nil))
	if recorder.Code != http.StatusOK || strings.TrimSpace(recorder.Body.String()) != `{"work":null}` {
		t.Fatalf("work status = %d %s", recorder.Code, recorder.Body.String())
	}
}

func TestCurrentWorkRouteIsAbsentWithoutProvider(t *testing.T) {
	_, addr, creds := startTestServer(t, &scriptedRunner{}, events.NewBus())
	client := insecureClient()
	defer client.CloseIdleConnections()
	req, err := http.NewRequest(http.MethodGet, "https://"+addr+"/v1/work", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+creds.Token)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}
