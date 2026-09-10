package netapi

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

func TestParseBinaryAudioChunk(t *testing.T) {
	pcm := []byte{0x00, 0x01, 0x00, 0x80}
	raw := marshalBinaryAudioChunk(7, 24000, pcm)
	seg, rate, got, ok := ParseBinaryAudioChunk(raw)
	if !ok || seg != 7 || rate != 24000 {
		t.Fatalf("parse ok=%v seg=%d rate=%d", ok, seg, rate)
	}
	if string(got) != string(pcm) {
		t.Fatalf("pcm = %v, want %v", got, pcm)
	}
	if _, _, _, ok := ParseBinaryAudioChunk([]byte(`{"type":"audio_chunk"}`)); ok {
		t.Fatal("JSON parsed as binary")
	}
}

func TestDecodePCMS16LEBytesRejectsBadFrames(t *testing.T) {
	if _, err := decodePCMS16LEBytes(nil, 16000); err == nil {
		t.Fatal("empty accepted")
	}
	if _, err := decodePCMS16LEBytes([]byte{1}, 16000); err == nil {
		t.Fatal("odd length accepted")
	}
	if _, err := decodePCMS16LEBytes([]byte{0, 0}, 8000); err == nil {
		t.Fatal("non-16k accepted")
	}
	huge := make([]byte, maxBinaryUplinkBytes+2)
	if _, err := decodePCMS16LEBytes(huge, 16000); err == nil {
		t.Fatal("oversize accepted")
	}
}

func TestAudioFanoutBinaryChunksWhenOptedIn(t *testing.T) {
	h := newHub()
	jsonConn := newTestConn(16, 16)
	binConn := newTestConn(16, 16)
	h.add(jsonConn)
	h.add(binConn)
	h.setConnAudio(jsonConn, true)
	h.setConnAudio(binConn, true)
	binConn.audioBinary.Store(true)

	fanout := NewAudioFanout(nil)
	fanout.AttachHub(h)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	stream := bufferedPCM(t, ctx, 24000, []float32{0.5, -0.5, 0.25})
	playback, err := fanout.PlayStream(ctx, stream)
	if err != nil {
		t.Fatalf("PlayStream: %v", err)
	}
	<-playback.Done()

	jsonMsg := <-jsonConn.audio
	if jsonMsg[0] != '{' {
		t.Fatalf("JSON client got non-JSON %q", jsonMsg[:min(12, len(jsonMsg))])
	}
	var env map[string]any
	if err := json.Unmarshal(jsonMsg, &env); err != nil || env["type"] != "audio_chunk" {
		t.Fatalf("JSON client envelope = %s err=%v", jsonMsg, err)
	}

	binMsg := <-binConn.audio
	seg, rate, pcm, ok := ParseBinaryAudioChunk(binMsg)
	if !ok || rate != 24000 || len(pcm) != 6 {
		t.Fatalf("binary chunk ok=%v seg=%d rate=%d pcm=%d", ok, seg, rate, len(pcm))
	}
}

func TestBinaryUplinkReachesIngress(t *testing.T) {
	runner := &voiceRunner{
		drain:      true,
		voiceStart: make(chan struct{}, 1),
		voiceDone:  make(chan struct{}, 1),
	}
	_, _, addr, creds := startVoiceServer(t, runner)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ws := dialStream(t, ctx, addr, creds)

	writeControl(t, ctx, ws, controlMessage{Type: "voice_start"})
	select {
	case <-runner.voiceStart:
	case <-ctx.Done():
		t.Fatal("voice turn never started")
	}

	const samples = 160
	raw := make([]byte, samples*2)
	for i := range raw {
		raw[i] = byte(i % 251)
	}
	if err := ws.Write(ctx, websocket.MessageBinary, raw); err != nil {
		t.Fatal(err)
	}
	writeControl(t, ctx, ws, controlMessage{Type: "voice_end"})
	select {
	case <-runner.voiceDone:
	case <-ctx.Done():
		t.Fatal("voice turn never finished")
	}
	if got := runner.samplesConsumed(); got != samples {
		t.Fatalf("consumed %d samples, want %d", got, samples)
	}
}

func TestBinaryUplinkWithoutClaimIsRefused(t *testing.T) {
	runner := &voiceRunner{}
	_, _, addr, creds := startVoiceServer(t, runner)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ws := dialStream(t, ctx, addr, creds)
	frames := readFrames(t, ctx, ws)

	if err := ws.Write(ctx, websocket.MessageBinary, []byte{0, 0}); err != nil {
		t.Fatal(err)
	}
	if got := frames.await("error", 2*time.Second); !contains(got, "error") {
		t.Fatalf("unclaimed binary uplink frames = %v, want error", got)
	}
}

func TestBinaryDownlinkOnOptIn(t *testing.T) {
	bus := events.NewBus()
	creds, err := LoadOrCreateCredentials(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	fanout := NewAudioFanout(nil)
	d := NewDispatcher(&scriptedRunner{runs: make(chan struct{}, 1)}, bus, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go d.Run(ctx)

	s := New(Options{
		Bind:        "127.0.0.1:0",
		Credentials: creds,
		Bus:         bus,
		Dispatcher:  d,
		Audio:       fanout,
	})
	go func() { _ = s.ListenAndServe(ctx) }()
	deadline := time.After(2 * time.Second)
	for s.Addr() == nil {
		select {
		case <-deadline:
			t.Fatal("server never bound")
		case <-time.After(5 * time.Millisecond):
		}
	}

	header := http.Header{}
	header.Set("Authorization", "Bearer "+creds.Token)
	ws, _, err := websocket.Dial(ctx, "wss://"+s.Addr().String()+"/v1/stream", &websocket.DialOptions{
		HTTPClient: insecureClient(),
		HTTPHeader: header,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close(websocket.StatusNormalClosure, "")

	optIn, _ := json.Marshal(controlMessage{Type: "audio_output", Mode: "stream", Binary: true})
	if err := ws.Write(ctx, websocket.MessageText, optIn); err != nil {
		t.Fatal(err)
	}

	read := func(timeout time.Duration) (websocket.MessageType, []byte, error) {
		rctx, rcancel := context.WithTimeout(ctx, timeout)
		defer rcancel()
		return ws.Read(rctx)
	}

	// Wait for the ack so the hub has marked this client as a streamer
	// before PlayStream encodes. Same handshake as the JSON opt-in test.
	var acked bool
	for range 8 {
		typ, data, err := read(2 * time.Second)
		if err != nil {
			t.Fatalf("audio_output_ack: %v", err)
		}
		if typ == websocket.MessageBinary {
			t.Fatal("binary frame before audio_output_ack")
		}
		var env map[string]any
		_ = json.Unmarshal(data, &env)
		if env["type"] == "audio_output_ack" && env["mode"] == "stream" && env["binary"] == true {
			acked = true
			break
		}
	}
	if !acked {
		t.Fatal("no audio_output_ack with binary=true")
	}

	pcmStream := bufferedPCM(t, ctx, 24000, []float32{0.9, -0.9})
	if _, err := fanout.PlayStream(ctx, pcmStream); err != nil {
		t.Fatal(err)
	}

	for range 8 {
		typ, data, err := read(2 * time.Second)
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		if typ == websocket.MessageBinary {
			_, rate, pcm, ok := ParseBinaryAudioChunk(data)
			if !ok || rate != 24000 || len(pcm) != 4 {
				t.Fatalf("binary TTS ok=%v rate=%d pcm=%d", ok, rate, len(pcm))
			}
			return
		}
		var env map[string]any
		_ = json.Unmarshal(data, &env)
		if env["type"] == "audio_chunk" {
			t.Fatal("binary-opted client received JSON audio_chunk")
		}
	}
	t.Fatal("binary-opted client never received an OVA1 frame")
}
