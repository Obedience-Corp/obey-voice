package speechserve

import (
	"context"
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// streamingChildScript is a Stage B worker: several pcm_meta + PCM + newline
// groups per synthesize, each numbered, then a final carrying the totals.
// It also advertises streaming_capable, and honours the request's "stream"
// field so the bridge can be shown forwarding it unchanged.
const streamingChildScript = `#!/usr/bin/env python3
import json, struct, sys

log_path = sys.argv[1]
chunks = int(sys.argv[2]) if len(sys.argv) > 2 else 3
per_chunk = int(sys.argv[3]) if len(sys.argv) > 3 else 2


def log(raw):
    with open(log_path, "a") as handle:
        handle.write(raw.rstrip("\n") + "\n")


def emit(obj):
    sys.stdout.write(json.dumps(obj) + "\n")
    sys.stdout.flush()


emit({
    "type": "ready", "protocol": "qwen3-tts-worker/v1", "sample_rate": 24000,
    "pcm_format": "f32le", "streaming": False, "streaming_capable": True,
    "presets": ["Vivian"], "note": "stage_A_whole_utterance_pcm_after_synth",
})

while True:
    raw = sys.stdin.readline()
    if raw == "":
        break
    log(raw)
    try:
        msg = json.loads(raw)
    except ValueError:
        continue
    kind = msg.get("type")
    if kind == "shutdown":
        break
    if kind != "synthesize":
        continue
    request = msg.get("id", "")
    if not msg.get("stream"):
        # Stage A: one blob, no chunk fields, exactly as before.
        emit({"type": "pcm_meta", "id": request, "sample_rate": 24000,
              "format": "f32le", "n_samples": chunks * per_chunk})
        sys.stdout.buffer.write(struct.pack("<%df" % (chunks * per_chunk),
                                            *[0.1 * (i + 1) for i in range(chunks * per_chunk)]))
        sys.stdout.buffer.write(b"\n")
        sys.stdout.buffer.flush()
        emit({"type": "final", "id": request})
        continue
    total = 0
    for index in range(chunks):
        emit({"type": "pcm_meta", "id": request, "sample_rate": 24000,
              "format": "f32le", "n_samples": per_chunk, "chunk": index})
        values = [0.1 * (index * per_chunk + i + 1) for i in range(per_chunk)]
        sys.stdout.buffer.write(struct.pack("<%df" % per_chunk, *values))
        sys.stdout.buffer.write(b"\n")
        sys.stdout.buffer.flush()
        total += per_chunk
    emit({"type": "final", "id": request, "chunks": chunks, "n_samples": total})
`

func startStreamingBridge(t *testing.T, args ...string) *testBridge {
	t.Helper()
	dir := t.TempDir()
	script := filepath.Join(dir, "qwen3-tts-worker")
	if err := os.WriteFile(script, []byte(streamingChildScript), 0o700); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(dir, "child.log")
	if err := os.WriteFile(logPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	server, err := New(Options{
		Bind:         "127.0.0.1:0",
		Token:        testToken,
		Child:        ChildSpec{Binary: script, ModelDir: dir, Tier: "0.6b", Args: append([]string{logPath}, args...)},
		Tier:         "0.6b",
		ReadyTimeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	if err := server.Start(ctx); err != nil {
		cancel()
		t.Fatalf("Start: %v", err)
	}
	go func() { _ = server.Serve(ctx) }()
	t.Cleanup(func() {
		cancel()
		server.Close()
	})
	return &testBridge{Server: server, logPath: logPath}
}

// The worker kept the Stage A frame shape so a proxy written before streaming
// existed would carry chunks without changing. This proves it does: the bridge
// forwards every numbered pcm_meta, its payload and its trailing newline, and
// the streamed final's totals, without knowing any of those fields exist.
func TestBridgeForwardsStreamedChunksVerbatim(t *testing.T) {
	bridge := startStreamingBridge(t, "4", "3")
	client := dialBridge(t, bridge.Addr())
	ready := client.hello(t, testToken)
	if ready["streaming_capable"] != true {
		t.Fatalf("ready did not carry the worker's capability through: %v", ready)
	}

	client.send(t, `{"type":"synthesize","id":"nqwen-1","text":"hello","stream":true}`)

	var got []float32
	for index := range 4 {
		meta := client.frame(t)
		if meta["type"] != "pcm_meta" {
			t.Fatalf("frame %d = %v, want pcm_meta", index, meta)
		}
		if meta["chunk"] != float64(index) {
			t.Fatalf("chunk index = %v, want %d: the bridge reordered or dropped a frame", meta["chunk"], index)
		}
		if meta["n_samples"] != float64(3) {
			t.Fatalf("n_samples = %v, want 3", meta["n_samples"])
		}
		raw := client.readPCM(t, 3)
		for i := range 3 {
			got = append(got, math.Float32frombits(binary.LittleEndian.Uint32(raw[i*4:])))
		}
	}

	final := client.frame(t)
	if final["type"] != "final" {
		t.Fatalf("final frame = %v", final)
	}
	if final["chunks"] != float64(4) || final["n_samples"] != float64(12) {
		t.Fatalf("final totals = %v: the integrity fields did not survive the proxy", final)
	}
	if len(got) != 12 {
		t.Fatalf("read %d samples across the chunks, want 12", len(got))
	}
	for i, sample := range got {
		if want := 0.1 * float32(i+1); math.Abs(float64(sample-want)) > 1e-5 {
			t.Fatalf("sample %d = %v, want %v: the payloads were interleaved wrong", i, sample, want)
		}
	}
}

// The bridge must hand the request's stream field to the child unchanged.
// Rewriting or dropping it would let the daemon's own environment decide
// whether a host that has ruled streaming out gets it anyway.
func TestBridgeForwardsTheStreamRequestFieldUnchanged(t *testing.T) {
	bridge := startStreamingBridge(t, "2", "2")
	client := dialBridge(t, bridge.Addr())
	client.hello(t, testToken)

	client.send(t, `{"type":"synthesize","id":"nqwen-1","text":"hello","stream":false}`)
	meta := client.frame(t)
	if meta["type"] != "pcm_meta" {
		t.Fatalf("frame = %v", meta)
	}
	if _, streamed := meta["chunk"]; streamed {
		t.Fatalf("stream:false still produced a chunked response: %v", meta)
	}
	client.readPCM(t, int(meta["n_samples"].(float64)))
	if final := client.frame(t); final["type"] != "final" {
		t.Fatalf("final = %v", final)
	}

	saw := bridge.childSaw(t)
	if !strings.Contains(saw, `"stream": false`) && !strings.Contains(saw, `"stream":false`) {
		t.Fatalf("child never saw the stream field: %s", saw)
	}
}
