package speechserve

import (
	"strings"
	"testing"
	"time"
)

// waitForSlot dials and says hello until the bridge answers with ready.
func waitForSlot(t *testing.T, addr string) *speechClient {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		client := dialBridge(t, addr)
		frame := client.hello(t, testToken)
		if frame["type"] == "ready" {
			return client
		}
		if time.Now().After(deadline) {
			t.Fatalf("the bridge never freed the client slot (last frame %v)", frame)
		}
		_ = client.conn.Close()
		time.Sleep(20 * time.Millisecond)
	}
}

// Regression, reported in review: the client slot used to be freed the moment
// a socket closed, while the worker was still answering that client's request.
// The next client to authenticate then received the previous request's
// pcm_meta and PCM — someone else's audio, on someone else's connection.
func TestBridgeNeverLeaksFramesToTheNextClient(t *testing.T) {
	// The worker ignores cancel, so the abandoned request really does answer
	// with pcm_meta + PCM + final while a second client is connected — the
	// exact shape of the reported leak, PCM bytes included.
	bridge := startTestBridge(t, "1", "ignore-cancel")

	victim := dialBridge(t, bridge.Addr())
	victim.hello(t, testToken)
	victim.send(t, `{"type":"synthesize","id":"victim-1","text":"a long reply"}`)
	// Let the request reach the worker and stay in flight.
	time.Sleep(200 * time.Millisecond)
	_ = victim.conn.Close()

	next := waitForSlot(t, bridge.Addr())
	next.send(t, `{"type":"synthesize","id":"next-1","text":"mine"}`)

	// Read this client's whole exchange. Nothing in it may belong to victim-1.
	for {
		frame := next.frame(t)
		if id, ok := frame["id"].(string); ok && id == "victim-1" {
			t.Fatalf("the next client received the previous client's frame: %v", frame)
		}
		switch frame["type"] {
		case "pcm_meta":
			if frame["id"] != "next-1" {
				t.Fatalf("pcm_meta belongs to %v", frame["id"])
			}
			next.readPCM(t, 3)
		case "final":
			if frame["id"] != "next-1" {
				t.Fatalf("final belongs to %v", frame["id"])
			}
			return
		case "error":
			t.Fatalf("unexpected error frame: %v", frame)
		}
	}
}

// The other half of the same fix: while the abandoned request is still being
// answered, the slot stays held. A worker that ignores cancel must not let the
// next client in mid-request.
func TestBridgeHoldsTheSlotUntilTheAbandonedRequestEnds(t *testing.T) {
	bridge := startTestBridge(t, "1", "ignore-cancel")

	victim := dialBridge(t, bridge.Addr())
	victim.hello(t, testToken)
	victim.send(t, `{"type":"synthesize","id":"victim-1","text":"uninterruptible"}`)
	time.Sleep(200 * time.Millisecond)
	_ = victim.conn.Close()

	frame, err := tryHello(bridge.Addr())
	if err != nil {
		t.Fatalf("tryHello: %v", err)
	}
	if frame["message"] != msgBusy {
		t.Fatalf("frame=%v, want the slot still held by the abandoned request", frame)
	}

	// Once the worker finishes, the slot comes back and the new client's
	// exchange is its own.
	next := waitForSlot(t, bridge.Addr())
	next.send(t, `{"type":"synthesize","id":"next-1","text":"mine"}`)
	meta := next.frame(t)
	if meta["type"] != "pcm_meta" || meta["id"] != "next-1" {
		t.Fatalf("frame=%v", meta)
	}
	if saw := bridge.childSaw(t); !strings.Contains(saw, `"cancel"`) {
		t.Fatalf("the abandoned request was never cancelled: %q", saw)
	}
}

// Cancel must actually interrupt: the worker answers with an error instead of
// the audio it would otherwise have produced.
func TestBridgeCancelAbortsSynthesisInFlight(t *testing.T) {
	bridge := startTestBridge(t, "2")

	client := dialBridge(t, bridge.Addr())
	client.hello(t, testToken)
	client.send(t, `{"type":"synthesize","id":"nqwen-1","text":"interrupt me"}`)
	time.Sleep(200 * time.Millisecond)
	client.send(t, `{"type":"cancel","id":"nqwen-1"}`)

	frame := client.frame(t)
	if frame["type"] != "error" || frame["id"] != "nqwen-1" {
		t.Fatalf("frame=%v, want the aborted request to answer with an error", frame)
	}
	saw := bridge.childSaw(t)
	if !strings.Contains(saw, `"cancel"`) {
		t.Fatalf("cancel never reached the child: %q", saw)
	}
	if strings.Index(saw, `"cancel"`) < strings.Index(saw, `"synthesize"`) {
		t.Fatalf("cancel arrived before the request: %q", saw)
	}

	// The connection is still usable: one aborted request is not a dead session.
	client.send(t, `{"type":"synthesize","id":"nqwen-2","text":"again"}`)
	meta := client.frame(t)
	if meta["type"] != "pcm_meta" || meta["id"] != "nqwen-2" {
		t.Fatalf("frame=%v", meta)
	}
}

// spec/30: extra ready fields are ignored by clients, not stripped by the
// bridge. A worker that learns a new field must be able to tell the host.
func TestBridgeReadyPassesUnknownChildFieldsThrough(t *testing.T) {
	bridge := startTestBridge(t, "0")
	client := dialBridge(t, bridge.Addr())

	ready := client.hello(t, testToken)
	if ready["engine_build"] != "fake-engine-1" {
		t.Fatalf("ready dropped an unknown child field: %v", ready)
	}
	if ready["protocol"] != "obey-speech/v1" || ready["tier"] != "0.6b" {
		t.Fatalf("ready=%v", ready)
	}
}

// A daemon whose worker died must say so, rather than accepting connections
// and going quiet. The host's doctor row then reports the real reason.
func TestBridgeReportsAStoppedWorker(t *testing.T) {
	bridge := startTestBridge(t, "0")
	if err := bridge.child.cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		frame, err := tryHello(bridge.Addr())
		if err == nil && frame["message"] == msgWorkerStopped {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("a dead worker was never reported (frame=%v err=%v)", frame, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
