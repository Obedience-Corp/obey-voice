package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"
	"time"
)

func TestCatalogDoesNotBlockControlsAndKeepsRequestIDs(t *testing.T) {
	input, requests := io.Pipe()
	defer input.Close()
	defer requests.Close()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	started := make(chan string, 2)
	release := map[string]chan struct{}{"first": make(chan struct{}), "second": make(chan struct{})}
	catalog := func(ctx context.Context, raw json.RawMessage) (any, error) {
		var name string
		if err := json.Unmarshal(raw, &name); err != nil {
			return nil, err
		}
		started <- name
		select {
		case <-release[name]:
			if name == "first" {
				return nil, errors.New("daemon unavailable")
			}
			return name, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	writer := &eventWriter{messages: make(chan map[string]any, 20)}
	done := make(chan error, 1)
	go func() { done <- runWithCatalog(ctx, input, writer, catalog) }()
	t.Cleanup(func() { cancel(); awaitCatalogSignal(t, done) })
	enc := json.NewEncoder(requests)
	for i, name := range []string{"first", "second"} {
		raw, err := json.Marshal(name)
		if err != nil {
			t.Fatal(err)
		}
		if err := enc.Encode(request{ID: uint64(i + 1), Method: "obey_catalog", Config: raw}); err != nil {
			t.Fatal(err)
		}
		if got := awaitCatalogSignal(t, started); got != name {
			t.Fatalf("started %q, want %q", got, name)
		}
	}
	// Neither lookup can finish yet. A live-control command must still reply.
	if err := enc.Encode(request{ID: 3, Method: "persona_preview_stop"}); err != nil {
		t.Fatal(err)
	}
	writer.until(t, func(v map[string]any) bool { return v["id"] == float64(3) })
	close(release["second"])
	second := writer.until(t, func(v map[string]any) bool { return v["id"] == float64(2) })
	if second["result"] != "second" || second["error"] != nil {
		t.Fatalf("second reply: %+v", second)
	}
	close(release["first"])
	first := writer.until(t, func(v map[string]any) bool { return v["id"] == float64(1) })
	if first["error"] != "daemon unavailable" {
		t.Fatalf("first reply: %+v", first)
	}
}

func TestCatalogCanceledAndJoinedOnBridgeExit(t *testing.T) {
	for _, shutdown := range []string{"shutdown", "eof", "parent"} {
		t.Run(shutdown, func(t *testing.T) {
			input, requests := io.Pipe()
			defer input.Close()
			defer requests.Close()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			started, canceled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
			catalog := func(ctx context.Context, _ json.RawMessage) (any, error) {
				close(started)
				<-ctx.Done()
				close(canceled)
				<-release // Simulate subprocess cleanup after cancellation.
				return nil, ctx.Err()
			}
			writer := &eventWriter{messages: make(chan map[string]any, 20)}
			done := make(chan error, 1)
			go func() { done <- runWithCatalog(ctx, input, writer, catalog) }()
			t.Cleanup(func() { cancel(); close(release); awaitCatalogSignal(t, done) })
			enc := json.NewEncoder(requests)
			if err := enc.Encode(request{ID: 7, Method: "obey_catalog", Config: json.RawMessage(`{}`)}); err != nil {
				t.Fatal(err)
			}
			awaitCatalogSignal(t, started)
			switch shutdown {
			case "shutdown":
				if err := enc.Encode(request{Method: "shutdown"}); err != nil {
					t.Fatal(err)
				}
			case "eof":
				if err := requests.Close(); err != nil {
					t.Fatal(err)
				}
			case "parent":
				cancel()
			}
			awaitCatalogSignal(t, canceled)
			select {
			case <-done:
				t.Fatal("bridge exited before catalog cleanup")
			default:
			}
		})
	}
}

func awaitCatalogSignal[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case value := <-ch:
		return value
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for catalog lifecycle")
		var zero T
		return zero
	}
}
