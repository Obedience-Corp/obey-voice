package netapi

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/coder/websocket"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

type snapshotTestRunner struct{}

func (snapshotTestRunner) RunTurnTextMode(context.Context, string) error { return nil }

func testSnapshot() SessionSnapshot {
	return SessionSnapshot{
		SessionID:         "session-a",
		ConversationEpoch: "epoch-a",
		Selection: events.PersonaSelection{
			SessionID: "session-a", ConversationEpoch: "epoch-a", PersonaID: "santa",
			DisplayName: "Santa", BindingHash: "binding-a", Revision: 4,
		},
		Transcript: []brain.Turn{{Role: "assistant", Content: "hello", ResponseID: "response-a"}},
	}
}

func TestSessionSnapshotFirstFrameAndResponseIDs(t *testing.T) {
	h := newHub()
	d := NewDispatcher(snapshotTestRunner{}, events.NewBus(), nil, nil)
	s := &Server{dispatcher: d, hub: h}
	conn := &streamConn{out: make(chan []byte, connQueueDepth), audio: make(chan []byte, audioQueueDepth), kick: make(chan string, 1)}

	if err := s.installSnapshot(context.Background(), conn, testSnapshot()); err != nil {
		t.Fatal(err)
	}
	h.broadcast([]byte(`{"type":"response_ready","response_envelope":{"response_id":"response-a"}}`))
	first := <-conn.out
	second := <-conn.out
	var snapshot struct {
		Type       string       `json:"type"`
		Transcript []brain.Turn `json:"transcript"`
	}
	if err := json.Unmarshal(first, &snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.Type != "session_snapshot" || len(snapshot.Transcript) != 1 || snapshot.Transcript[0].ResponseID != "response-a" {
		t.Fatalf("first frame = %s, want canonical session snapshot with response id", first)
	}
	if string(second) == string(first) {
		t.Fatal("event was not queued after the snapshot")
	}
}

func TestDispatcherSnapshotCancellationDoesNotInstall(t *testing.T) {
	d := NewDispatcher(snapshotTestRunner{}, events.NewBus(), nil, nil)
	runCtx, stopRun := context.WithCancel(context.Background())
	defer stopRun()
	go d.Run(runCtx)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var installed atomic.Int32
	err := d.Snapshot(ctx, func(context.Context) (SessionSnapshot, error) {
		t.Fatal("canceled snapshot provider ran")
		return SessionSnapshot{}, nil
	}, func(context.Context, SessionSnapshot) error {
		installed.Add(1)
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Snapshot error = %v, want context canceled", err)
	}
	if installed.Load() != 0 {
		t.Fatal("canceled snapshot registered a connection")
	}
}

func TestDispatcherSnapshotProviderFailureDoesNotInstall(t *testing.T) {
	d := NewDispatcher(snapshotTestRunner{}, events.NewBus(), nil, nil)
	runCtx, stopRun := context.WithCancel(context.Background())
	defer stopRun()
	go d.Run(runCtx)

	want := errors.New("snapshot failed")
	var installed atomic.Int32
	err := d.Snapshot(context.Background(), func(context.Context) (SessionSnapshot, error) {
		return SessionSnapshot{}, want
	}, func(context.Context, SessionSnapshot) error {
		installed.Add(1)
		return nil
	})
	if !errors.Is(err, want) {
		t.Fatalf("Snapshot error = %v, want %v", err, want)
	}
	if installed.Load() != 0 {
		t.Fatal("failed snapshot registered a connection")
	}
}

func TestInstallSnapshotCanceledDoesNotRegister(t *testing.T) {
	d := NewDispatcher(snapshotTestRunner{}, events.NewBus(), nil, nil)
	h := newHub()
	s := &Server{dispatcher: d, hub: h}
	conn := &streamConn{out: make(chan []byte, connQueueDepth), audio: make(chan []byte, audioQueueDepth), kick: make(chan string, 1)}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.installSnapshot(ctx, conn, testSnapshot()); !errors.Is(err, context.Canceled) {
		t.Fatalf("install error = %v, want context canceled", err)
	}
	if len(h.conns) != 0 {
		t.Fatal("canceled install registered a connection")
	}
}

func TestPublishSnapshotEvictionReclaimsStreamingClient(t *testing.T) {
	h := newHub()
	s := &Server{
		hub: h,
		opts: Options{CurrentSessionSnapshot: func(context.Context) (SessionSnapshot, error) {
			return testSnapshot(), nil
		}},
	}
	conn := &streamConn{
		out: make(chan []byte, connQueueDepth), audio: make(chan []byte, audioQueueDepth), kick: make(chan string, 1),
	}
	if err := s.installSnapshot(context.Background(), conn, testSnapshot()); err != nil {
		t.Fatal(err)
	}
	h.setConnAudio(conn, true, false)
	for range connQueueDepth - 1 {
		conn.out <- []byte("queued")
	}
	if !h.hasStreamClients() {
		t.Fatal("streaming client was not counted")
	}

	s.publishSnapshot(context.Background())

	if len(h.conns) != 0 {
		t.Fatalf("connections after eviction = %d, want 0", len(h.conns))
	}
	if h.hasStreamClients() {
		t.Fatal("evicted streaming client kept the streamer count alive")
	}
	select {
	case reason := <-conn.kick:
		if reason != "client too slow" {
			t.Fatalf("eviction reason = %q, want client too slow", reason)
		}
	default:
		t.Fatal("snapshot eviction did not kick the slow client")
	}

	// The handler's deferred cleanup runs after the publish path. It must be a
	// no-op for the already detached client and must not decrement twice.
	h.remove(conn)
	if h.streamers.Load() != 0 {
		t.Fatalf("streamers after deferred cleanup = %d, want 0", h.streamers.Load())
	}

	replacement := &streamConn{
		out: make(chan []byte, connQueueDepth), audio: make(chan []byte, audioQueueDepth), kick: make(chan string, 1),
	}
	if !h.add(replacement) {
		t.Fatal("hub did not reclaim capacity after snapshot eviction")
	}
	h.setConnAudio(replacement, true, false)
	if h.streamers.Load() != 1 {
		t.Fatalf("replacement streamer count = %d, want 1", h.streamers.Load())
	}
	h.remove(replacement)
	if h.streamers.Load() != 0 {
		t.Fatalf("streamers after replacement cleanup = %d, want 0", h.streamers.Load())
	}
}

func TestSnapshotConnectCapacityErrorsAreRetryable(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		reason string
	}{
		{name: "dispatcher queue", err: ErrBusy, reason: "server busy — try again shortly"},
		{name: "connection cap", err: errTooManyStreamClients, reason: "too many clients"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status, reason := snapshotConnectClose(tt.err)
			if status != websocket.StatusTryAgainLater {
				t.Fatalf("status = %v, want %v", status, websocket.StatusTryAgainLater)
			}
			if reason != tt.reason {
				t.Fatalf("reason = %q, want %q", reason, tt.reason)
			}
		})
	}
}

func TestSnapshotConnectCapacityClosesWebSocketRetryably(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	creds, err := LoadOrCreateCredentials(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	d := NewDispatcher(snapshotTestRunner{}, events.NewBus(), nil, nil)
	srv := New(Options{
		Bind:        "127.0.0.1:0",
		Credentials: creds,
		Bus:         events.NewBus(),
		Dispatcher:  d,
		CurrentSessionSnapshot: func(context.Context) (SessionSnapshot, error) {
			return testSnapshot(), nil
		},
	})
	for range maxStreamClients {
		if !srv.hub.add(&streamConn{
			out: make(chan []byte, connQueueDepth), audio: make(chan []byte, audioQueueDepth), kick: make(chan string, 1),
		}) {
			t.Fatal("could not fill stream client capacity")
		}
	}
	go d.Run(ctx)
	done := make(chan error, 1)
	go func() { done <- srv.ListenAndServe(ctx) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("server exit: %v", err)
		}
	})
	addr := waitAddr(t, srv)
	ws, _, err := websocket.Dial(ctx, "wss://"+addr+"/v1/stream", &websocket.DialOptions{
		HTTPClient: insecureClient(),
		HTTPHeader: http.Header{"Authorization": []string{"Bearer " + creds.Token}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ws.CloseNow() }()
	_, _, err = ws.Read(ctx)
	var closeErr websocket.CloseError
	if !errors.As(err, &closeErr) {
		t.Fatalf("read error = %v, want websocket close", err)
	}
	if closeErr.Code != websocket.StatusTryAgainLater {
		t.Fatalf("close status = %v, want %v", closeErr.Code, websocket.StatusTryAgainLater)
	}
}

func TestSnapshotConnectDispatchQueueClosesWebSocketRetryably(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	creds, err := LoadOrCreateCredentials(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	d := NewDispatcher(snapshotTestRunner{}, events.NewBus(), nil, nil)
	for range dispatchQueueDepth {
		if err := d.SubmitText("queued"); err != nil {
			t.Fatal(err)
		}
	}
	srv := New(Options{
		Bind:        "127.0.0.1:0",
		Credentials: creds,
		Bus:         events.NewBus(),
		Dispatcher:  d,
		CurrentSessionSnapshot: func(context.Context) (SessionSnapshot, error) {
			return testSnapshot(), nil
		},
	})
	done := make(chan error, 1)
	go func() { done <- srv.ListenAndServe(ctx) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("server exit: %v", err)
		}
	})
	addr := waitAddr(t, srv)
	ws, _, err := websocket.Dial(ctx, "wss://"+addr+"/v1/stream", &websocket.DialOptions{
		HTTPClient: insecureClient(),
		HTTPHeader: http.Header{"Authorization": []string{"Bearer " + creds.Token}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ws.CloseNow() }()
	_, _, err = ws.Read(ctx)
	var closeErr websocket.CloseError
	if !errors.As(err, &closeErr) {
		t.Fatalf("read error = %v, want websocket close", err)
	}
	if closeErr.Code != websocket.StatusTryAgainLater {
		t.Fatalf("close status = %v, want %v", closeErr.Code, websocket.StatusTryAgainLater)
	}
}

func TestDispatcherPublishesBoundarySnapshots(t *testing.T) {
	bus := events.NewBus()
	var published atomic.Int32
	d := NewDispatcher(snapshotTestRunner{}, bus, func() {}, func(string) error { return nil })
	d.setSnapshotPublisher(func(context.Context) { published.Add(1) })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go d.Run(ctx)

	if err := d.ClearHistory(); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(time.Second)
	for published.Load() == 0 {
		select {
		case <-deadline:
			t.Fatal("clear did not publish a session snapshot")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	if err := d.ResumeSession(context.Background(), "session-b"); err != nil {
		t.Fatal(err)
	}
	if published.Load() != 2 {
		t.Fatalf("published snapshots = %d, want clear and resume", published.Load())
	}
}

func TestMicTransitionAdvancesSharedStateRevision(t *testing.T) {
	d := NewDispatcher(snapshotTestRunner{}, events.NewBus(), nil, nil)
	h := newHub()
	h.setStateRevision(func() { d.bumpStateRevision() })
	conn := &streamConn{}
	h.micClaimant = conn
	before := d.StateRevision()
	h.mu.Lock()
	h.releaseLocked(conn)
	h.mu.Unlock()
	if got := d.StateRevision(); got <= before {
		t.Fatalf("state revision after mic release = %d, was %d", got, before)
	}
}

func TestCapabilitiesGateSessionSnapshot(t *testing.T) {
	legacy := CapabilitiesForSnapshot(false)
	if legacy[CapSharedPersonaSnapshot] || legacy[CapResponseIDDedupe] {
		t.Fatalf("legacy capabilities claim snapshot support: %v", legacy)
	}
	current := CapabilitiesForSnapshot(true)
	if !current[CapSharedPersonaSnapshot] || !current[CapResponseIDDedupe] {
		t.Fatalf("snapshot capabilities missing: %v", current)
	}
}

func TestSnapshotProviderCanceledWhenDispatcherStops(t *testing.T) {
	d := NewDispatcher(snapshotTestRunner{}, events.NewBus(), nil, nil)
	runCtx, stop := context.WithCancel(context.Background())
	defer stop()
	go d.Run(runCtx)
	entered := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- d.Snapshot(context.Background(), func(ctx context.Context) (SessionSnapshot, error) {
			close(entered)
			<-ctx.Done()
			return SessionSnapshot{}, ctx.Err()
		}, func(context.Context, SessionSnapshot) error {
			t.Error("canceled provider installed snapshot")
			return nil
		})
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("provider did not start")
	}
	stop()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("dispatcher shutdown stranded snapshot provider")
	}
}

func TestSessionSnapshotWireSecondClientReconnectAndClear(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	creds, err := LoadOrCreateCredentials(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	bus := events.NewBus()
	current := testSnapshot() // Only dispatcher callbacks access mutable host state.
	d := NewDispatcher(snapshotTestRunner{}, bus, func() {
		current.ConversationEpoch = "epoch-b"
		current.Selection.ConversationEpoch = "epoch-b"
		current.Selection.Revision++
		current.Transcript = nil
	}, nil)
	srv := New(Options{Bind: "127.0.0.1:0", Credentials: creds, Bus: bus, Dispatcher: d,
		CurrentSessionSnapshot: func(context.Context) (SessionSnapshot, error) { return current, nil },
		SetPersonaContext: func(_ context.Context, id string) (PersonaAck, error) {
			current.Selection.PersonaID = id
			current.Selection.DisplayName = "Samantha"
			current.Selection.Revision++
			bus.Emit(current.Selection)
			return PersonaAck{ID: id, DisplayName: "Samantha"}, nil
		},
	})
	go d.Run(ctx)
	done := make(chan error, 1)
	go func() { done <- srv.ListenAndServe(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	addr := waitAddr(t, srv)
	read := func(ws *websocket.Conn) map[string]json.RawMessage {
		t.Helper()
		_, data, err := ws.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var msg map[string]json.RawMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			t.Fatal(err)
		}
		return msg
	}
	connect := func() (*websocket.Conn, SessionSnapshot) {
		t.Helper()
		ws, _, err := websocket.Dial(ctx, "wss://"+addr+"/v1/stream", &websocket.DialOptions{HTTPClient: insecureClient(), HTTPHeader: http.Header{"Authorization": []string{"Bearer " + creds.Token}}})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = ws.CloseNow() })
		msg := read(ws)
		if string(msg["type"]) != `"session_snapshot"` {
			t.Fatalf("first message = %s", msg["type"])
		}
		data, _ := json.Marshal(msg)
		var snap SessionSnapshot
		if err := json.Unmarshal(data, &snap); err != nil {
			t.Fatal(err)
		}
		return ws, snap
	}
	first, initial := connect()
	if len(initial.Transcript) != 1 || initial.Transcript[0].ResponseID != "response-a" {
		t.Fatalf("initial transcript = %+v", initial)
	}
	if err := first.Write(ctx, websocket.MessageText, []byte(`{"type":"set_persona","name":"samantha"}`)); err != nil {
		t.Fatal(err)
	}
	selected := read(first)
	if string(selected["type"]) != `"persona_selected"` {
		t.Fatalf("selection = %s", selected["type"])
	}
	second, observed := connect()
	if observed.Selection.PersonaID != "samantha" || observed.StateRevision <= initial.StateRevision || len(observed.Transcript) != 1 {
		t.Fatalf("second client snapshot = %+v", observed)
	}
	_ = second.CloseNow()
	third, reconnected := connect()
	if reconnected.Selection.PersonaID != "samantha" || len(reconnected.Transcript) != 1 || reconnected.Transcript[0].ResponseID != "response-a" {
		t.Fatalf("reconnect = %+v", reconnected)
	}
	if err := third.Write(ctx, websocket.MessageText, []byte(`{"type":"clear_history"}`)); err != nil {
		t.Fatal(err)
	}
	cleared := read(third)
	if string(cleared["type"]) != `"conversation_cleared"` {
		t.Fatalf("clear = %s", cleared["type"])
	}
	next := read(third)
	if string(next["type"]) != `"session_snapshot"` {
		t.Fatalf("clear snapshot = %s", next["type"])
	}
	data, _ := json.Marshal(next)
	var after SessionSnapshot
	if err := json.Unmarshal(data, &after); err != nil {
		t.Fatal(err)
	}
	if after.ConversationEpoch != "epoch-b" || len(after.Transcript) != 0 || after.StateRevision <= reconnected.StateRevision {
		t.Fatalf("after clear = %+v", after)
	}
}
