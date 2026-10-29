package debugstream

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

func TestLiveSourceIsPrivateReadOnlyAndRecorderIndependent(t *testing.T) {
	root := t.TempDir()
	bus := events.NewBus()
	trace := events.TraceContext{RuntimeID: "runtime", SessionID: "session", ConversationEpoch: "epoch"}
	s, err := Start(root, "Fixture voice", bus, trace)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	d := s.Descriptor()
	stat, err := os.Stat(filepath.Join(root, d.ID+".json"))
	if err != nil || stat.Mode().Perm() != 0600 {
		t.Fatal("metadata permissions", stat, err)
	}
	client := &http.Client{Timeout: 2 * time.Second}
	defer client.CloseIdleConnections()
	request := func(method, path, token, origin string) *http.Response {
		t.Helper()
		req, _ := http.NewRequest(method, d.Endpoint+path, nil)
		req.Header.Set("Authorization", token)
		req.Header.Set("Origin", origin)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}
	for _, tc := range []struct {
		method, path, token, origin string
		code                        int
	}{
		{"GET", "/health", "", "", 401}, {"GET", "/events", "Bearer wrong", "", 401},
		{"GET", "/events", "Bearer " + d.Token, "http://example.invalid", 401},
		{"POST", "/events", "Bearer " + d.Token, "", 405},
	} {
		resp := request(tc.method, tc.path, tc.token, tc.origin)
		resp.Body.Close()
		if resp.StatusCode != tc.code {
			t.Fatal(resp.StatusCode, tc)
		}
	}
	resp := request("GET", "/health", "Bearer "+d.Token, "")
	var got Descriptor
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if got.ID != d.ID || got.RuntimeID != "runtime" || got.Token != "" {
		t.Fatalf("health %+v", got)
	}
	bus.Emit(events.UserInput{Text: "before attachment", Trace: &trace})
	// Independent viewers get retained evidence, then new events, without a recorder.
	for range 2 {
		resp = request("GET", "/events", "Bearer "+d.Token, "")
		reader := bufio.NewScanner(resp.Body)
		reader.Buffer(make([]byte, 4096), 2<<20)
		foundHeader, foundInput := false, false
		for reader.Scan() {
			raw := reader.Text()
			foundHeader = foundHeader || strings.Contains(raw, `"trace_schema":"obey-voice.trace/v1"`)
			if strings.Contains(raw, "before attachment") {
				foundInput = true
				break
			}
		}
		resp.Body.Close()
		if !foundHeader || !foundInput {
			t.Fatal("late reader lost header/history", reader.Err())
		}
	}
	files, _ := os.ReadDir(root)
	if len(files) != 1 || files[0].Name() != d.ID+".json" {
		t.Fatal("unexpected persisted content", files)
	}
	s.Close()
	s.Close()
	if _, err := os.Stat(filepath.Join(root, d.ID+".json")); !os.IsNotExist(err) {
		t.Fatal("advertisement survived close", err)
	}
	// Detachment leaves the conversation bus usable.
	bus.Emit(events.UserInput{Text: "after disconnect", Trace: &trace})
}

func TestHistoryBoundsPreserveHeaderAndExposeSequenceGap(t *testing.T) {
	h := history{changed: make(chan struct{})}
	h.publish([]byte(`{"type":"header","seq":1}`))
	payload := strings.Repeat("x", 8192)
	for i := 2; i < 2000; i++ {
		h.publish([]byte(fmt.Sprintf(`{"type":"agent_delta","seq":%d,"text":%q}`, i, payload)))
	}
	packets, _ := h.after(0)
	if h.bytes > historyBytes || len(h.records) > historyRecords || len(packets) < 2 || packets[0].seq != 1 || packets[1].seq <= 2 {
		t.Fatal("history is unbounded or loss hidden")
	}
}

func TestReaderLimitAndSlowConsumerDoNotBlockBus(t *testing.T) {
	bus := events.NewBus()
	trace := events.TraceContext{RuntimeID: "runtime"}
	s, err := Start(t.TempDir(), "fixture", bus, trace)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client := &http.Client{}
	defer client.CloseIdleConnections()
	for i := 0; i <= maxReaders; i++ {
		req, _ := http.NewRequestWithContext(ctx, "GET", s.Descriptor().Endpoint+"/events", nil)
		req.Header.Set("Authorization", "Bearer "+s.Descriptor().Token)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		want := 200
		if i == maxReaders {
			want = 503
		}
		if resp.StatusCode != want {
			t.Fatal(resp.StatusCode, want)
		}
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range 4000 {
			bus.Emit(events.ResponseDelta{Text: strings.Repeat("x", 4096), Trace: &trace})
		}
	}()
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("slow readers stalled bus")
	}
}
