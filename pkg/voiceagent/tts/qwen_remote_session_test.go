package tts

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

// fakeSpeechServer is speech-serve's wire, not its implementation: hello/auth,
// one ready, then the child's own framing forwarded verbatim.
type fakeSpeechServer struct {
	listener net.Listener
	token    string
	ready    string
	samples  []float32

	mu       sync.Mutex
	requests []map[string]any
	// helloSeen records the raw first line so a test can assert the child would
	// never have received it.
	helloSeen string
	// hangBeforeReady holds the connection open without answering.
	hangBeforeReady bool
	// chunks, when set, answers a stream:true request Stage B style: one
	// numbered pcm_meta per chunk, then a final carrying the totals. Empty
	// leaves every existing test on the whole-blob path unchanged.
	chunks [][]float32
}

func startFakeSpeechServer(t *testing.T, srv *fakeSpeechServer) *fakeSpeechServer {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv.listener = listener
	if srv.ready == "" {
		srv.ready = `{"type":"ready","protocol":"obey-speech/v1","sample_rate":24000,"pcm_format":"f32le","streaming":false,"presets":["Vivian"],"tier":"0.6b"}`
	}
	if srv.samples == nil {
		srv.samples = []float32{0.5, -0.5, 0.25}
	}
	go srv.serve()
	t.Cleanup(func() { _ = listener.Close() })
	return srv
}

func (s *fakeSpeechServer) url() string { return "http://" + s.listener.Addr().String() }

func (s *fakeSpeechServer) serve() {
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return
		}
		go s.handle(conn)
	}
}

func (s *fakeSpeechServer) handle(conn net.Conn) {
	defer conn.Close()
	reader := bufio.NewReader(conn)
	line, err := reader.ReadString('\n')
	if err != nil {
		return
	}
	s.mu.Lock()
	s.helloSeen = strings.TrimSpace(line)
	s.mu.Unlock()

	var hello config.SpeechHello
	if err := json.Unmarshal([]byte(line), &hello); err != nil ||
		hello.Type != "hello" || hello.Protocol != config.SpeechProtocol || hello.Token != s.token {
		fmt.Fprint(conn, `{"type":"error","message":"unauthorized"}`+"\n")
		return
	}
	if s.hangBeforeReady {
		// Hold the connection open without answering, but not past the test run.
		time.Sleep(10 * time.Second)
		return
	}
	fmt.Fprint(conn, s.ready+"\n")

	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return
		}
		var msg map[string]any
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			return
		}
		s.mu.Lock()
		s.requests = append(s.requests, msg)
		s.mu.Unlock()
		switch msg["type"] {
		case "synthesize":
			id, _ := msg["id"].(string)
			if wantStream, _ := msg["stream"].(bool); wantStream && len(s.chunks) > 0 {
				s.writeChunks(conn, id)
				continue
			}
			fmt.Fprintf(conn, `{"type":"pcm_meta","id":%q,"sample_rate":24000,"format":"f32le","n_samples":%d}`+"\n", id, len(s.samples))
			raw := make([]byte, len(s.samples)*4)
			for i, sample := range s.samples {
				binary.LittleEndian.PutUint32(raw[i*4:], math.Float32bits(sample))
			}
			_, _ = conn.Write(raw)
			_, _ = conn.Write([]byte("\n"))
			fmt.Fprintf(conn, `{"type":"final","id":%q}`+"\n", id)
		case "shutdown":
			return
		}
	}
}

// writeChunks answers Stage B style, framed exactly as the worker frames it.
func (s *fakeSpeechServer) writeChunks(conn net.Conn, id string) {
	total := 0
	for index, chunk := range s.chunks {
		fmt.Fprintf(conn, `{"type":"pcm_meta","id":%q,"sample_rate":24000,"format":"f32le","n_samples":%d,"chunk":%d}`+"\n",
			id, len(chunk), index)
		raw := make([]byte, len(chunk)*4)
		for i, sample := range chunk {
			binary.LittleEndian.PutUint32(raw[i*4:], math.Float32bits(sample))
		}
		_, _ = conn.Write(raw)
		_, _ = conn.Write([]byte("\n"))
		total += len(chunk)
	}
	fmt.Fprintf(conn, `{"type":"final","id":%q,"chunks":%d,"n_samples":%d}`+"\n", id, len(s.chunks), total)
}

func (s *fakeSpeechServer) seen() (string, []map[string]any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.helloSeen, append([]map[string]any(nil), s.requests...)
}

func lanConfig(url, token string) *config.Config {
	return &config.Config{
		TTSProvider:     "qwen3-tts",
		TTSPlace:        config.TTSPlaceLAN,
		TTSWorkerURL:    url,
		TTSWorkerToken:  token,
		TTSLanTimeoutMS: 2000,
		QwenTTSTimeout:  30,
	}
}

func TestDialRemoteQwenSessionErrors(t *testing.T) {
	closed := startFakeSpeechServer(t, &fakeSpeechServer{token: "s3cret"})
	closedURL := closed.url()
	_ = closed.listener.Close()

	for _, tc := range []struct {
		name string
		cfg  func(t *testing.T) *config.Config
		want string
	}{
		{
			name: "unreachable",
			cfg:  func(*testing.T) *config.Config { return lanConfig(closedURL, "s3cret") },
			want: "dial speech worker",
		},
		{
			name: "bad token",
			cfg: func(t *testing.T) *config.Config {
				srv := startFakeSpeechServer(t, &fakeSpeechServer{token: "s3cret"})
				return lanConfig(srv.url(), "wrong")
			},
			want: "unauthorized",
		},
		{
			name: "wrong ready protocol",
			cfg: func(t *testing.T) *config.Config {
				srv := startFakeSpeechServer(t, &fakeSpeechServer{
					token: "s3cret",
					ready: `{"type":"ready","protocol":"qwen3-tts-worker/v1","sample_rate":24000,"pcm_format":"f32le"}`,
				})
				return lanConfig(srv.url(), "s3cret")
			},
			want: "protocol",
		},
		{
			name: "wrong sample rate",
			cfg: func(t *testing.T) *config.Config {
				srv := startFakeSpeechServer(t, &fakeSpeechServer{
					token: "s3cret",
					ready: `{"type":"ready","protocol":"obey-speech/v1","sample_rate":16000,"pcm_format":"f32le"}`,
				})
				return lanConfig(srv.url(), "s3cret")
			},
			want: "sample rate",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := dialRemoteQwenSession(context.Background(), tc.cfg(t))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err=%v want %q", err, tc.want)
			}
			if !IsProviderErrorKind(err, ProviderErrorUnavailable) {
				t.Fatalf("err=%v is not an unavailable provider error", err)
			}
		})
	}
}

func TestDialRemoteQwenSessionCancelledContext(t *testing.T) {
	srv := startFakeSpeechServer(t, &fakeSpeechServer{token: "s3cret", hangBeforeReady: true})
	cfg := lanConfig(srv.url(), "s3cret")
	cfg.TTSLanTimeoutMS = 10000

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	_, err := dialRemoteQwenSession(ctx, cfg)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v want context.Canceled", err)
	}
}

func TestRemoteQwenSessionSynthesizes(t *testing.T) {
	srv := startFakeSpeechServer(t, &fakeSpeechServer{token: "s3cret"})
	session, err := dialRemoteQwenSession(context.Background(), lanConfig(srv.url(), "s3cret"))
	if err != nil {
		t.Fatalf("dialRemoteQwenSession: %v", err)
	}
	defer session.Close()

	if session.Streaming() {
		t.Fatal("stage A ready must not advertise streaming")
	}
	if got := session.Presets(); len(got) != 1 || got[0] != "Vivian" {
		t.Fatalf("presets=%v", got)
	}
	if session.Tier() != "0.6b" {
		t.Fatalf("tier=%q", session.Tier())
	}

	got := collectSynthesis(t, session, SynthesisRequest{Text: "hi", Voice: "Vivian", ReferenceAudio: "/tmp/throat.wav"})
	if len(got) != 3 {
		t.Fatalf("samples=%v want 3", got)
	}

	hello, requests := srv.seen()
	if !strings.Contains(hello, `"type":"hello"`) || !strings.Contains(hello, config.SpeechProtocol) {
		t.Fatalf("hello=%q", hello)
	}
	if len(requests) != 1 {
		t.Fatalf("requests=%v", requests)
	}
	if requests[0]["preset"] != "Vivian" || requests[0]["text"] != "hi" {
		t.Fatalf("synthesize=%v", requests[0])
	}
	if _, sent := requests[0]["ref_wav"]; sent {
		t.Fatal("ref_wav must never cross the LAN wire")
	}
}

func TestRemoteQwenSessionCancelMidStream(t *testing.T) {
	srv := startFakeSpeechServer(t, &fakeSpeechServer{token: "s3cret"})
	session, err := dialRemoteQwenSession(context.Background(), lanConfig(srv.url(), "s3cret"))
	if err != nil {
		t.Fatalf("dialRemoteQwenSession: %v", err)
	}
	defer session.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	stream := audio.NewPCMStream(context.Background())
	if err := session.SynthesizeToStream(ctx, SynthesisRequest{Text: "hi"}, stream); !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v want context.Canceled", err)
	}
}

func TestNewQwen3TTSLanUsesTheSpeechWorker(t *testing.T) {
	srv := startFakeSpeechServer(t, &fakeSpeechServer{token: "s3cret"})
	cfg := lanConfig(srv.url(), "s3cret")
	// A shipped clone is the shipped default: over the LAN it must degrade to
	// the worker's own voice rather than fail every turn.
	cfg.QwenTTSVoice = "Veronica"

	q, err := NewQwen3TTS(cfg)
	if err != nil {
		t.Fatalf("NewQwen3TTS: %v", err)
	}
	defer q.Delete()

	if !q.remote || !q.native {
		t.Fatalf("remote=%v native=%v", q.remote, q.native)
	}
	if q.Capabilities().SupportsReferenceAudio {
		t.Fatal("the LAN wire carries no ref_wav; it must not be advertised")
	}
	if _, err := q.SynthesizeRequest(context.Background(), SynthesisRequest{Text: "hi", ReferenceAudio: "/tmp/throat.wav"}); !IsProviderErrorKind(err, ProviderErrorInput) {
		t.Fatalf("ref_wav over lan: err=%v want an input error", err)
	}

	result, err := q.SynthesizeRequest(context.Background(), SynthesisRequest{Text: "hello there"})
	if err != nil {
		t.Fatalf("SynthesizeRequest: %v", err)
	}
	var n int
	for frame := range result.Stream.Frames() {
		n += len(frame)
	}
	if n != 3 {
		t.Fatalf("got %d samples", n)
	}
	_, requests := srv.seen()
	if len(requests) != 1 {
		t.Fatalf("requests=%v", requests)
	}
	if _, sent := requests[0]["preset"]; sent {
		t.Fatalf("a clone voice must not be sent as a preset: %v", requests[0])
	}
}

func TestNewQwen3TTSLanUnreachableIsUnavailable(t *testing.T) {
	srv := startFakeSpeechServer(t, &fakeSpeechServer{token: "s3cret"})
	url := srv.url()
	_ = srv.listener.Close()

	_, err := NewQwen3TTS(lanConfig(url, "s3cret"))
	if !IsProviderErrorKind(err, ProviderErrorUnavailable) {
		t.Fatalf("err=%v want an unavailable provider error", err)
	}
	// No local worker was resolved: the host needs no binary or GGUF.
	if strings.Contains(fmt.Sprint(err), "models ensure") {
		t.Fatalf("lan construction consulted local assets: %v", err)
	}
}

func collectSynthesis(t *testing.T, session qwenSpeechSession, req SynthesisRequest) []float32 {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	stream := audio.NewPCMStream(ctx)
	errCh := make(chan error, 1)
	go func() {
		err := session.SynthesizeToStream(ctx, req, stream)
		if err != nil {
			stream.CloseWithError(err)
		} else {
			stream.Close()
		}
		errCh <- err
	}()
	var got []float32
	for frame := range stream.Frames() {
		got = append(got, frame...)
	}
	if err := <-errCh; err != nil {
		t.Fatalf("SynthesizeToStream: %v", err)
	}
	return got
}
