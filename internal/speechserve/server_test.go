package speechserve

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

// fakeChildScript is a worker that speaks the frozen child protocol: ready
// unprompted, one pcm_meta + raw f32le + final per synthesize, exit on
// shutdown. Every line it receives is appended to logPath, so a test can prove
// what the bridge did and did not forward.
//
// Synthesis is interruptible: while a request is "generating" it keeps reading
// stdin, so a cancel really aborts the request instead of arriving after the
// audio was already emitted. The ready line carries an unknown field on
// purpose — the bridge must pass it through.
const fakeChildScript = `#!/usr/bin/env python3
import json, select, struct, sys, time

log_path = sys.argv[1]
window = float(sys.argv[2]) if len(sys.argv) > 2 else 0.0
ignore_cancel = len(sys.argv) > 3 and sys.argv[3] == "ignore-cancel"


def log(raw):
    with open(log_path, "a") as handle:
        handle.write(raw.rstrip("\n") + "\n")


def emit(obj):
    sys.stdout.write(json.dumps(obj) + "\n")
    sys.stdout.flush()


def read_line(timeout=None):
    ready, _, _ = select.select([sys.stdin], [], [], timeout)
    if not ready:
        return None
    raw = sys.stdin.readline()
    if raw == "":
        raise SystemExit(0)
    log(raw)
    try:
        return json.loads(raw)
    except ValueError:
        return {}


emit({
    "type": "ready", "protocol": "qwen3-tts-worker/v1", "sample_rate": 24000,
    "pcm_format": "f32le", "streaming": False, "presets": ["Vivian"],
    "note": "stage_A", "engine_build": "fake-engine-1",
})

while True:
    msg = read_line()
    if msg is None:
        continue
    kind = msg.get("type")
    if kind == "shutdown":
        break
    if kind != "synthesize":
        continue
    request = msg.get("id", "")
    deadline = time.time() + window
    aborted = False
    while time.time() < deadline:
        nxt = read_line(timeout=0.05)
        if nxt is None:
            continue
        if nxt.get("type") == "shutdown":
            raise SystemExit(0)
        if nxt.get("type") == "cancel" and not ignore_cancel:
            aborted = True
            break
    if aborted:
        emit({"type": "error", "id": request, "message": "canceled"})
        continue
    emit({"type": "pcm_meta", "id": request, "sample_rate": 24000, "format": "f32le", "n_samples": 3})
    sys.stdout.buffer.write(struct.pack("<fff", 0.5, -0.5, 0.25))
    sys.stdout.buffer.write(b"\n")
    sys.stdout.buffer.flush()
    emit({"type": "final", "id": request})
`

const testToken = "s3cret-token"

type testBridge struct {
	*Server
	logPath string
}

// startTestBridge runs the real bridge over a scripted child on loopback.
// window is how many seconds a synthesize stays in flight before answering;
// extra passes further arguments to the child (e.g. "ignore-cancel").
func startTestBridge(t *testing.T, window string, extra ...string) *testBridge {
	t.Helper()
	dir := t.TempDir()
	script := filepath.Join(dir, "qwen3-tts-worker")
	if err := os.WriteFile(script, []byte(fakeChildScript), 0o700); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(dir, "child.log")
	if err := os.WriteFile(logPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	server, err := New(Options{
		Bind:  "127.0.0.1:0",
		Token: testToken,
		Child: ChildSpec{Binary: script, ModelDir: dir, Tier: "0.6b", Args: append([]string{logPath, window}, extra...)},
		Tier:  "0.6b",
		// The scripted child is ready at once; a real one takes seconds.
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
	server.listener = observedListener{server.listener}
	go func() { _ = server.Serve(ctx) }()
	t.Cleanup(func() {
		cancel()
		server.Close()
	})
	return &testBridge{Server: server, logPath: logPath}
}

func (b *testBridge) childSaw(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(b.logPath)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// speechClient is a raw obey-speech/v1 client: the wire, with nothing helpful
// in the way.
type speechClient struct {
	conn   net.Conn
	reader *bufio.Reader
}

func dialBridge(t *testing.T, addr string) *speechClient {
	t.Helper()
	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if err := conn.SetDeadline(time.Now().Add(15 * time.Second)); err != nil {
		t.Fatal(err)
	}
	return &speechClient{conn: conn, reader: bufio.NewReader(conn)}
}

func (c *speechClient) hello(t *testing.T, token string) map[string]any {
	t.Helper()
	line, err := config.SpeechHelloLine(token)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.conn.Write(line); err != nil {
		t.Fatalf("write hello: %v", err)
	}
	return c.frame(t)
}

func (c *speechClient) send(t *testing.T, raw string) {
	t.Helper()
	if _, err := c.conn.Write([]byte(raw + "\n")); err != nil {
		t.Fatalf("write %s: %v", raw, err)
	}
}

func (c *speechClient) frame(t *testing.T) map[string]any {
	t.Helper()
	line, err := c.reader.ReadString('\n')
	if err != nil {
		t.Fatalf("read frame: %v", err)
	}
	var frame map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(line)), &frame); err != nil {
		t.Fatalf("frame %q: %v", line, err)
	}
	return frame
}

// readPCM consumes the payload a pcm_meta announced, plus its trailing newline.
func (c *speechClient) readPCM(t *testing.T, samples int) []byte {
	t.Helper()
	buf := make([]byte, samples*4)
	if _, err := readFull(c.reader, buf); err != nil {
		t.Fatalf("read pcm: %v", err)
	}
	if b, err := c.reader.ReadByte(); err == nil && b != '\n' {
		_ = c.reader.UnreadByte()
	}
	return buf
}

func readFull(r *bufio.Reader, buf []byte) (int, error) {
	read := 0
	for read < len(buf) {
		n, err := r.Read(buf[read:])
		read += n
		if err != nil {
			return read, err
		}
	}
	return read, nil
}

func TestNewRefusesUnsafeBinds(t *testing.T) {
	spec := ChildSpec{Binary: "/opt/qwen3-tts-worker", ModelDir: "/opt/models"}
	for _, tc := range []struct {
		name string
		opts Options
		want string
	}{
		{
			name: "no token",
			opts: Options{Bind: "127.0.0.1:7263", Child: spec},
			want: "requires a token",
		},
		{
			name: "public bind without tls",
			opts: Options{Bind: "0.0.0.0:7263", Token: testToken, Child: spec},
			want: "without TLS",
		},
		{
			name: "lan bind without tls",
			opts: Options{Bind: "192.168.1.10:7263", Token: testToken, Child: spec},
			want: "without TLS",
		},
		{
			name: "bare port is not loopback",
			opts: Options{Bind: ":7263", Token: testToken, Child: spec},
			want: "without TLS",
		},
		{
			name: "malformed bind",
			opts: Options{Bind: "127.0.0.1", Token: testToken, Child: spec},
			want: "host:port",
		},
		{
			name: "no worker",
			opts: Options{Bind: "127.0.0.1:7263", Token: testToken},
			want: "models ensure",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := New(tc.opts)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err=%v want %q", err, tc.want)
			}
		})
	}
}

func TestNewAcceptsLoopbackAndTLSBinds(t *testing.T) {
	spec := ChildSpec{Binary: "/opt/qwen3-tts-worker", ModelDir: "/opt/models"}
	for _, tc := range []struct {
		name string
		opts Options
	}{
		{name: "default bind", opts: Options{Token: testToken, Child: spec}},
		{name: "ipv6 loopback", opts: Options{Bind: "[::1]:7263", Token: testToken, Child: spec}},
		{name: "localhost", opts: Options{Bind: "localhost:7263", Token: testToken, Child: spec}},
		{name: "public with tls", opts: Options{Bind: "0.0.0.0:7263", Token: testToken, Child: spec, TLSConfig: &tls.Config{}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server, err := New(tc.opts)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			if server.opts.Bind == "" {
				t.Fatal("bind must default to loopback")
			}
		})
	}
}

func TestBridgeRefusesBadHandshakes(t *testing.T) {
	for _, tc := range []struct {
		name  string
		first string
	}{
		{name: "wrong token", first: `{"type":"hello","protocol":"obey-speech/v1","token":"nope"}`},
		{name: "missing token", first: `{"type":"hello","protocol":"obey-speech/v1"}`},
		{name: "wrong protocol", first: `{"type":"hello","protocol":"qwen3-tts-worker/v1","token":"s3cret-token"}`},
		{name: "synthesize first", first: `{"type":"synthesize","id":"1","text":"hi"}`},
		{name: "not json", first: `hello?`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bridge := startTestBridge(t, "0")
			client := dialBridge(t, bridge.Addr())
			client.send(t, tc.first)

			frame := client.frame(t)
			if frame["type"] != "error" || frame["message"] != msgUnauthorized {
				t.Fatalf("frame=%v want an unauthorized error", frame)
			}
			if _, err := client.reader.ReadString('\n'); err == nil {
				t.Fatal("the connection must close after a refusal")
			}
			if saw := bridge.childSaw(t); strings.Contains(saw, "hello") || strings.Contains(saw, "synthesize") {
				t.Fatalf("the child saw a refused client's traffic: %q", saw)
			}
		})
	}
}

func TestBridgeReadyRewritesProtocolAndAddsTier(t *testing.T) {
	bridge := startTestBridge(t, "0")
	client := dialBridge(t, bridge.Addr())

	ready := client.hello(t, testToken)
	if ready["type"] != "ready" || ready["protocol"] != config.SpeechProtocol {
		t.Fatalf("ready=%v", ready)
	}
	if ready["sample_rate"] != float64(24000) || ready["pcm_format"] != "f32le" || ready["streaming"] != false {
		t.Fatalf("ready did not copy the child's fields: %v", ready)
	}
	if ready["note"] != "stage_A" {
		t.Fatalf("ready dropped the child's note: %v", ready)
	}
	presets, _ := ready["presets"].([]any)
	if len(presets) != 1 || presets[0] != "Vivian" {
		t.Fatalf("ready dropped the child's presets: %v", ready)
	}
	if ready["tier"] != "0.6b" {
		t.Fatalf("ready must carry this host's tier: %v", ready)
	}
	// The child speaks its own protocol and must never learn the TCP one.
	if saw := bridge.childSaw(t); strings.Contains(saw, "hello") {
		t.Fatalf("hello reached the child: %q", saw)
	}
}

func TestBridgeForwardsSynthesisFrameForFrame(t *testing.T) {
	bridge := startTestBridge(t, "0")
	client := dialBridge(t, bridge.Addr())
	client.hello(t, testToken)

	client.send(t, `{"type":"synthesize","id":"nqwen-1","text":"hello there","preset":"Vivian"}`)
	meta := client.frame(t)
	if meta["type"] != "pcm_meta" || meta["id"] != "nqwen-1" || meta["n_samples"] != float64(3) {
		t.Fatalf("pcm_meta=%v", meta)
	}
	pcm := client.readPCM(t, 3)
	if len(pcm) != 12 {
		t.Fatalf("pcm=%d bytes want 12", len(pcm))
	}
	if final := client.frame(t); final["type"] != "final" || final["id"] != "nqwen-1" {
		t.Fatalf("final=%v", final)
	}
	if saw := bridge.childSaw(t); !strings.Contains(saw, `"text":"hello there"`) {
		t.Fatalf("the child did not receive the request verbatim: %q", saw)
	}
}

// D20: a client's shutdown ends the connection, never the worker. Otherwise a
// doctor probe or a closed conversation would cold-kill a warm model.
func TestBridgeInterceptsClientShutdown(t *testing.T) {
	bridge := startTestBridge(t, "0")

	first := dialBridge(t, bridge.Addr())
	first.hello(t, testToken)
	first.send(t, `{"type":"shutdown"}`)
	if _, err := first.reader.ReadString('\n'); err == nil {
		t.Fatal("shutdown must close the connection")
	}
	if saw := bridge.childSaw(t); strings.Contains(saw, "shutdown") {
		t.Fatalf("shutdown was forwarded to the child: %q", saw)
	}

	// The worker is still warm: a new client synthesizes without a reload.
	second := dialBridge(t, bridge.Addr())
	second.hello(t, testToken)
	second.send(t, `{"type":"synthesize","id":"nqwen-2","text":"still here"}`)
	if meta := second.frame(t); meta["type"] != "pcm_meta" {
		t.Fatalf("frame=%v want pcm_meta from the same warm child", meta)
	}
	second.readPCM(t, 3)
	if final := second.frame(t); final["id"] != "nqwen-2" {
		t.Fatalf("final=%v", final)
	}
}

func TestBridgeRefusesASecondClient(t *testing.T) {
	bridge := startTestBridge(t, "0")
	first := dialBridge(t, bridge.Addr())
	first.hello(t, testToken)

	// Auth comes before the slot, so a second client is told it is welcome but
	// not alone — and an unauthenticated connection never holds the slot at all.
	second := dialBridge(t, bridge.Addr())
	frame := second.hello(t, testToken)
	if frame["type"] != "error" || frame["message"] != msgBusy {
		t.Fatalf("frame=%v want a busy refusal", frame)
	}
	if _, err := second.reader.ReadString('\n'); err == nil {
		t.Fatal("a refused second client must be closed")
	}

	// The first client is untouched by the refusal.
	first.send(t, `{"type":"synthesize","id":"nqwen-1","text":"mine"}`)
	if meta := first.frame(t); meta["type"] != "pcm_meta" {
		t.Fatalf("frame=%v", meta)
	}
}

func TestBridgeAllowsANewClientAfterTheFirstLeaves(t *testing.T) {
	bridge := startTestBridge(t, "0")
	first := dialBridge(t, bridge.Addr())
	first.hello(t, testToken)
	_ = first.conn.Close()

	// The slot is released when the server's reader unblocks, which is not
	// synchronous with the client's close.
	deadline := time.Now().Add(5 * time.Second)
	for {
		frame, err := tryHello(bridge.Addr())
		if err == nil && frame["type"] == "ready" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the client slot was never released (frame=%v err=%v)", frame, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// tryHello runs one full handshake and closes, reporting the first frame.
func tryHello(addr string) (map[string]any, error) {
	conn, err := net.DialTimeout("tcp", addr, time.Second)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
		return nil, err
	}
	hello, err := config.SpeechHelloLine(testToken)
	if err != nil {
		return nil, err
	}
	if _, err := conn.Write(hello); err != nil {
		return nil, err
	}
	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		return nil, err
	}
	var frame map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(line)), &frame); err != nil {
		return nil, err
	}
	return frame, nil
}

// One in flight per connection, enforced on the serve side too: a second
// request errors and the first still finishes.
func TestBridgeEnforcesOneInFlight(t *testing.T) {
	bridge := startTestBridge(t, "1")
	client := dialBridge(t, bridge.Addr())
	client.hello(t, testToken)

	client.send(t, `{"type":"synthesize","id":"nqwen-1","text":"slow"}`)
	client.send(t, `{"type":"synthesize","id":"nqwen-2","text":"too soon"}`)

	refusal := client.frame(t)
	if refusal["type"] != "error" || refusal["id"] != "nqwen-2" {
		t.Fatalf("frame=%v want an error naming the second request", refusal)
	}
	meta := client.frame(t)
	if meta["type"] != "pcm_meta" || meta["id"] != "nqwen-1" {
		t.Fatalf("frame=%v want the first request to continue", meta)
	}
	client.readPCM(t, 3)
	if final := client.frame(t); final["id"] != "nqwen-1" {
		t.Fatalf("final=%v", final)
	}
	if saw := bridge.childSaw(t); strings.Contains(saw, "too soon") {
		t.Fatalf("the refused request reached the child: %q", saw)
	}
}

func TestBridgeCancelsInFlightWhenTheClientDisappears(t *testing.T) {
	bridge := startTestBridge(t, "1")
	client := dialBridge(t, bridge.Addr())
	client.hello(t, testToken)
	client.send(t, `{"type":"synthesize","id":"nqwen-1","text":"abandoned"}`)
	time.Sleep(100 * time.Millisecond)
	_ = client.conn.Close()

	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(bridge.childSaw(t), `"cancel"`) {
		if time.Now().After(deadline) {
			t.Fatalf("no cancel reached the child: %q", bridge.childSaw(t))
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestServerStopsOnContextCancellation(t *testing.T) {
	bridge := startTestBridge(t, "0")
	addr := bridge.Addr()

	bridge.Close()
	if _, err := net.DialTimeout("tcp", addr, time.Second); err == nil {
		t.Fatal("the listener must be closed")
	}
}

func TestLoadOrMintToken(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "speech")

	token, created, err := LoadOrMintToken(dir)
	if err != nil {
		t.Fatalf("LoadOrMintToken: %v", err)
	}
	if !created || len(token) < 32 {
		t.Fatalf("token=%q created=%v", token, created)
	}
	info, err := os.Stat(filepath.Join(dir, TokenFileName))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("token file mode=%v want 0600", info.Mode().Perm())
	}

	again, created, err := LoadOrMintToken(dir)
	if err != nil {
		t.Fatalf("LoadOrMintToken: %v", err)
	}
	if created || again != token {
		t.Fatalf("second load minted a new token: %q vs %q", again, token)
	}

	if err := os.WriteFile(filepath.Join(dir, TokenFileName), []byte("\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadOrMintToken(dir); err == nil {
		t.Fatal("an empty token file must be an error, not an open door")
	}
}

func TestChildStartFailureIsReported(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "qwen3-tts-worker")
	if err := os.WriteFile(script, []byte("#!/usr/bin/env bash\nexit 3\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	server, err := New(Options{
		Bind:         "127.0.0.1:0",
		Token:        testToken,
		Child:        ChildSpec{Binary: script, ModelDir: dir, Args: []string{}},
		ReadyTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	err = server.Start(context.Background())
	if err == nil {
		server.Close()
		t.Fatal("expected a worker that cannot start to fail Start")
	}
	if !strings.Contains(err.Error(), "speech worker") {
		t.Fatalf("err=%v", err)
	}
	if addr := server.Addr(); addr != "" {
		t.Fatalf("nothing may be bound when the worker failed: %s", addr)
	}
	fmt.Fprint(os.Stderr, "")
}
