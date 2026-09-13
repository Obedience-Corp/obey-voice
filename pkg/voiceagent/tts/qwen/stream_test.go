package qwen

import (
	"context"
	"errors"
	tts "github.com/Obedience-Corp/obey-voice/pkg/voiceagent/tts"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

// streamedResponse frames a Stage B response exactly as the worker does:
// one pcm_meta per chunk carrying its 0-based index, N*4 raw f32le bytes, a
// newline, then a final line totalling the chunks and samples.
// (qwen3-tts-native tools/worker_main.c on_pcm_chunk / the streamed final.)
func streamedResponse(id string, chunks [][]float32) string {
	var b strings.Builder
	total := 0
	for i, chunk := range chunks {
		b.WriteString(`{"type":"pcm_meta","id":"` + id + `","sample_rate":24000,"format":"f32le","n_samples":` +
			itoa(len(chunk)) + `,"chunk":` + itoa(i) + "}\n")
		b.WriteString(pcmBytes(chunk...))
		b.WriteString("\n")
		total += len(chunk)
	}
	b.WriteString(`{"type":"final","id":"` + id + `","chunks":` + itoa(len(chunks)) +
		`,"n_samples":` + itoa(total) + "}\n")
	return b.String()
}

// closeableSpeechSession is the transport half with the one method a real
// transport adds, so the provider's policy can be exercised without a process
// or a socket.
type closeableSpeechSession struct{ *speechSession }

func (*closeableSpeechSession) Close() {}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}

// readInto drives one synthesis read against a scripted worker response.
func readInto(t *testing.T, wire string) ([]float32, error) {
	t.Helper()
	conn, _ := testConn(t, wire)
	session := &speechSession{conn: conn, label: nativeWorkerLabel, rate: qwen3TTSSampleRate}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	stream := audio.NewPCMStream(ctx)

	errCh := make(chan error, 1)
	go func() { errCh <- session.readSynthesis(ctx, "nqwen-1", stream) }()

	var got []float32
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		for frames := range stream.Frames() {
			got = append(got, frames...)
		}
	}()
	err := <-errCh
	stream.CloseWithError(err)
	<-drained
	return got, err
}

func TestReadSynthesisConcatenatesStreamedChunks(t *testing.T) {
	chunks := [][]float32{{0.1, 0.2}, {0.3, 0.4, 0.5}, {-0.6}}
	got, err := readInto(t, streamedResponse("nqwen-1", chunks))
	if err != nil {
		t.Fatalf("readSynthesis: %v", err)
	}
	want := []float32{0.1, 0.2, 0.3, 0.4, 0.5, -0.6}
	if len(got) != len(want) {
		t.Fatalf("read %d samples, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("sample %d = %v, want %v", i, got[i], want[i])
		}
	}
}

// The Stage A shape is what every worker in the field still sends, and the
// streamed reader must not have made it stricter.
func TestReadSynthesisAcceptsStageAWithNoChunkFields(t *testing.T) {
	wire := `{"type":"pcm_meta","id":"nqwen-1","sample_rate":24000,"format":"f32le","n_samples":3}` + "\n" +
		pcmBytes(0.5, -0.5, 0.25) + "\n" +
		`{"type":"final","id":"nqwen-1"}` + "\n"
	got, err := readInto(t, wire)
	if err != nil {
		t.Fatalf("readSynthesis: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("read %d samples, want 3", len(got))
	}
}

// A worker that streams chunks numbers them so a lost one is detectable. The
// payload lengths still line up when one goes missing, so nothing but the
// index would notice.
func TestReadSynthesisRejectsADroppedChunk(t *testing.T) {
	wire := `{"type":"pcm_meta","id":"nqwen-1","sample_rate":24000,"format":"f32le","n_samples":2,"chunk":0}` + "\n" +
		pcmBytes(0.1, 0.2) + "\n" +
		`{"type":"pcm_meta","id":"nqwen-1","sample_rate":24000,"format":"f32le","n_samples":2,"chunk":2}` + "\n" +
		pcmBytes(0.3, 0.4) + "\n" +
		`{"type":"final","id":"nqwen-1","chunks":3,"n_samples":6}` + "\n"
	_, err := readInto(t, wire)
	if err == nil {
		t.Fatal("a gap in the chunk numbering was accepted as complete audio")
	}
	if !strings.Contains(err.Error(), "dropped or reordered") {
		t.Fatalf("error %q does not name the dropped chunk", err)
	}
}

func TestReadSynthesisRejectsAFinalThatDisagreesOnCounts(t *testing.T) {
	for _, tc := range []struct {
		name  string
		final string
		want  string
	}{
		{
			name:  "chunk count",
			final: `{"type":"final","id":"nqwen-1","chunks":3,"n_samples":4}`,
			want:  "final reports 3 chunks, host read 2",
		},
		{
			name:  "sample total",
			final: `{"type":"final","id":"nqwen-1","chunks":2,"n_samples":9}`,
			want:  "final reports 9 samples, host read 4",
		},
		{
			name:  "totals stripped from a streamed response",
			final: `{"type":"final","id":"nqwen-1"}`,
			want:  "streamed 2 chunks but final carried no chunk total",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wire := `{"type":"pcm_meta","id":"nqwen-1","sample_rate":24000,"format":"f32le","n_samples":2,"chunk":0}` + "\n" +
				pcmBytes(0.1, 0.2) + "\n" +
				`{"type":"pcm_meta","id":"nqwen-1","sample_rate":24000,"format":"f32le","n_samples":2,"chunk":1}` + "\n" +
				pcmBytes(0.3, 0.4) + "\n" + tc.final + "\n"
			_, err := readInto(t, wire)
			if err == nil {
				t.Fatal("a final that disagrees with what was read was accepted")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q, want it to contain %q", err, tc.want)
			}
		})
	}
}

// A chunk whose payload is cut short is a truncated transport, not short audio.
func TestReadSynthesisRejectsAShortChunkPayload(t *testing.T) {
	wire := `{"type":"pcm_meta","id":"nqwen-1","sample_rate":24000,"format":"f32le","n_samples":4,"chunk":0}` + "\n" +
		pcmBytes(0.1, 0.2) // two of the four promised samples, then EOF
	_, err := readInto(t, wire)
	if err == nil {
		t.Fatal("a truncated chunk payload was accepted")
	}
	if !strings.Contains(err.Error(), "pcm read") {
		t.Fatalf("error %q does not name the short read", err)
	}
}

// A worker that dies between chunks must fail the request, not hang it.
func TestReadSynthesisFailsWhenTheWorkerDiesMidStream(t *testing.T) {
	wire := `{"type":"pcm_meta","id":"nqwen-1","sample_rate":24000,"format":"f32le","n_samples":2,"chunk":0}` + "\n" +
		pcmBytes(0.1, 0.2) + "\n" // no second chunk, no final: EOF
	_, err := readInto(t, wire)
	if err == nil {
		t.Fatal("EOF mid-stream was accepted as a complete utterance")
	}
}

// Cancelling mid-stream ends the read and leaves the session usable: the
// worker is told to abandon the request rather than being killed.
func TestReadSynthesisCancelMidStreamDoesNotWedgeTheSession(t *testing.T) {
	wire := streamedResponse("nqwen-1", [][]float32{{0.1, 0.2}, {0.3, 0.4}})
	conn, out := testConn(t, wire)
	session := &speechSession{conn: conn, label: nativeWorkerLabel, rate: qwen3TTSSampleRate}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	stream := audio.NewPCMStream(context.Background())
	err := session.readSynthesis(ctx, "nqwen-1", stream)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("readSynthesis = %v, want context.Canceled", err)
	}
	if !strings.Contains(out.String(), `"cancel"`) {
		t.Fatalf("no cancel was written for the in-flight request; wire=%q", out.String())
	}
	// The session is still open for the next request.
	if session.closed {
		t.Error("a cancelled read closed the session")
	}
}

// A capable worker is told explicitly in both directions. Its process default
// comes from QWEN3_TTS_STREAM in whatever environment started it, so silence
// from the host means "whatever that box was configured for" — which on a
// Metal box is exactly what the policy exists to prevent.
func TestSynthesizeRequestCarriesStreamOnlyForACapableWorker(t *testing.T) {
	for _, tc := range []struct {
		name    string
		capable bool
		stream  bool
		want    string
	}{
		{
			name:    "stage A worker sees the request it has always seen",
			capable: false, stream: false,
			want: `{"id":"nqwen-1","text":"hi","type":"synthesize"}`,
		},
		{
			name:    "capable worker this host streams from",
			capable: true, stream: true,
			want: `{"id":"nqwen-1","stream":true,"text":"hi","type":"synthesize"}`,
		},
		{
			name:    "capable worker this host has decided not to stream from",
			capable: true, stream: false,
			want: `{"id":"nqwen-1","stream":false,"text":"hi","type":"synthesize"}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wire := `{"type":"pcm_meta","id":"nqwen-1","sample_rate":24000,"format":"f32le","n_samples":1}` + "\n" +
				pcmBytes(0.1) + "\n" + `{"type":"final","id":"nqwen-1"}` + "\n"
			conn, out := testConn(t, wire)
			session := &speechSession{conn: conn, label: nativeWorkerLabel, rate: qwen3TTSSampleRate}
			session.capable = tc.capable
			session.applyStreamPolicy(tc.stream)

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			stream := audio.NewPCMStream(ctx)
			go func() {
				for range stream.Frames() {
				}
			}()
			if err := session.SynthesizeToStream(ctx, tts.SynthesisRequest{Text: "hi"}, stream); err != nil {
				t.Fatalf("SynthesizeToStream: %v", err)
			}
			stream.Close()
			if got := strings.TrimSpace(out.String()); got != tc.want {
				t.Fatalf("request line\n got %s\nwant %s", got, tc.want)
			}
		})
	}
}

// applyStreamPolicy must never let an incapable worker be marked streaming:
// the request field would be ignored and Capabilities would advertise a
// time-to-first-audio the wire cannot deliver.
func TestApplyStreamPolicyIgnoresAnIncapableWorker(t *testing.T) {
	session := &speechSession{label: nativeWorkerLabel}
	session.applyStreamPolicy(true)
	if session.StreamRequested() {
		t.Fatal("an incapable worker was marked as streaming")
	}
	session.capable = true
	session.applyStreamPolicy(true)
	if !session.StreamRequested() {
		t.Fatal("a capable worker was not marked as streaming")
	}
}

// The provider decides eligibility from the place it speaks through and the
// backend its installed package names. Metal is the measured failure.
func TestProviderStreamPolicyFollowsPlaceAndBackend(t *testing.T) {
	for _, tc := range []struct {
		name    string
		mode    string
		remote  bool
		backend string
		want    bool
	}{
		{name: "local metal stays whole-utterance", mode: config.QwenStreamAuto, backend: "metal", want: false},
		{name: "local cuda streams", mode: config.QwenStreamAuto, backend: "cuda", want: true},
		{name: "lan streams", mode: config.QwenStreamAuto, remote: true, want: true},
		{name: "off pins stage A over lan", mode: config.QwenStreamOff, remote: true, want: false},
		{name: "on overrides metal", mode: config.QwenStreamOn, backend: "metal", want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q := newQwen3TTS("", "", 0, nil)
			q.native = true
			q.remote = tc.remote
			q.streamMode = tc.mode
			q.backendHint = tc.backend

			session := &closeableSpeechSession{speechSession: &speechSession{label: nativeWorkerLabel}}
			session.capable = true
			q.applyStreamPolicy(session)

			if got := session.StreamRequested(); got != tc.want {
				t.Fatalf("StreamRequested = %v, want %v", got, tc.want)
			}
			q.speech = session
			var live qwenSpeechSession = session
			q.speechRef.Store(&live)
			if got := q.WorkerStreaming(); got != tc.want {
				t.Fatalf("WorkerStreaming = %v, want %v: the metric must describe what the wire does", got, tc.want)
			}
		})
	}
}

// streamingWorkerScript is a local Stage B child: it advertises capability and
// answers a stream:true request with numbered chunks, a stream:false one with
// the whole blob. Both framings come out of the same worker so the host's
// decision is the only thing that changes.
const streamingWorkerScript = `#!/usr/bin/env bash
set -euo pipefail
echo '{"type":"ready","protocol":"qwen3-tts-worker/v1","sample_rate":24000,"pcm_format":"f32le","streaming":false,"streaming_capable":true,"presets":["Vivian"]}'
while IFS= read -r line; do
  printf '%s\n' "$line" >>"$REQUEST_LOG"
  case "$line" in
    *'"shutdown"'*) exit 0 ;;
    *'"stream":true'*)
      python3 -c '
import struct, sys
for index, values in enumerate([(0.1, 0.2), (0.3, 0.4), (0.5, 0.6)]):
    sys.stdout.write("{\"type\":\"pcm_meta\",\"id\":\"nqwen-1\",\"sample_rate\":24000,\"format\":\"f32le\",\"n_samples\":2,\"chunk\":%d}\n" % index)
    sys.stdout.flush()
    sys.stdout.buffer.write(struct.pack("<ff", *values))
    sys.stdout.buffer.write(b"\n")
    sys.stdout.buffer.flush()
sys.stdout.write("{\"type\":\"final\",\"id\":\"nqwen-1\",\"chunks\":3,\"n_samples\":6}\n")
sys.stdout.flush()
'
      ;;
    *'"synthesize"'*)
      echo '{"type":"pcm_meta","id":"nqwen-1","sample_rate":24000,"format":"f32le","n_samples":6}'
      python3 -c 'import sys,struct; sys.stdout.buffer.write(struct.pack("<ffffff",0.1,0.2,0.3,0.4,0.5,0.6)); sys.stdout.buffer.write(b"\n")'
      echo '{"type":"final","id":"nqwen-1"}'
      ;;
  esac
done
`

// The local subprocess is the other place chunks have to arrive through, and
// it is the one where the backend decides: a cuda package streams, a metal one
// keeps whole utterances.
func TestLocalWorkerStreamsOnlyOnACUDAPackage(t *testing.T) {
	for _, tc := range []struct {
		name    string
		backend string
		want    bool
	}{
		{name: "cuda package streams", backend: config.QwenBackendCUDA, want: true},
		{name: "metal package does not", backend: "metal", want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			script := filepath.Join(dir, "qwen3-tts-worker")
			if err := os.WriteFile(script, []byte(streamingWorkerScript), 0o700); err != nil {
				t.Fatal(err)
			}
			log := filepath.Join(dir, "requests.log")
			t.Setenv("REQUEST_LOG", log)

			q, err := NewQwen3TTS(&config.Config{
				QwenTTSBinary:  script,
				QwenTTSModel:   t.TempDir(),
				QwenTTSVoice:   "Vivian",
				QwenTTSMode:    "customvoice",
				QwenTTSTimeout: 30,
			})
			if err != nil {
				t.Fatalf("NewQwen3TTS: %v", err)
			}
			defer q.Delete()
			// An explicit binary has no install manifest; the backend is what
			// the installed package would have reported.
			q.backendHint = tc.backend
			q.applyStreamPolicy(q.speech)

			if got := q.WorkerStreaming(); got != tc.want {
				t.Fatalf("WorkerStreaming = %v, want %v for a %s package", got, tc.want, tc.backend)
			}

			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			result, err := q.SynthesizeRequest(ctx, tts.SynthesisRequest{Text: "hello there"})
			if err != nil {
				t.Fatalf("SynthesizeRequest: %v", err)
			}
			var got []float32
			for frames := range result.Stream.Frames() {
				got = append(got, frames...)
			}
			if err := result.Stream.Err(); err != nil {
				t.Fatalf("stream error: %v", err)
			}
			if len(got) != 6 {
				t.Fatalf("read %d samples, want 6 whichever framing was used", len(got))
			}
			if result.Stream.Incremental() != tc.want {
				t.Fatalf("Incremental = %v, want %v", result.Stream.Incremental(), tc.want)
			}

			raw, err := os.ReadFile(log)
			if err != nil {
				t.Fatal(err)
			}
			want := `"stream":false`
			if tc.want {
				want = `"stream":true`
			}
			if !strings.Contains(string(raw), want) {
				t.Fatalf("worker never saw %s; it read:\n%s", want, raw)
			}
		})
	}
}

// End to end over the LAN wire: a capable speech worker, a host that decides
// to stream, chunks that reassemble in order, and a PCM stream marked so the
// player releases them as they arrive rather than buffering the sentence.
func TestRemoteProviderStreamsChunksAndMarksTheStream(t *testing.T) {
	srv := startFakeSpeechServer(t, &fakeSpeechServer{
		token: "s3cret",
		ready: `{"type":"ready","protocol":"obey-speech/v1","sample_rate":24000,"pcm_format":"f32le",` +
			`"streaming":false,"streaming_capable":true,"tier":"0.6b"}`,
		chunks: [][]float32{{0.1, 0.2}, {0.3, 0.4}, {0.5, 0.6}},
	})
	remote, err := newRemoteQwen3TTS(lanConfig(srv.url(), "s3cret"))
	if err != nil {
		t.Fatalf("newRemoteQwen3TTS: %v", err)
	}
	defer remote.Delete()

	if !remote.WorkerStreaming() {
		t.Fatal("lan defaults to whole-utterance: it is the measured CUDA offload target")
	}
	if !remote.Capabilities().SupportsStreaming {
		t.Error("Capabilities did not report the streaming this session actually does")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	result, err := remote.SynthesizeRequest(ctx, tts.SynthesisRequest{Text: "hello there"})
	if err != nil {
		t.Fatalf("SynthesizeRequest: %v", err)
	}
	var got []float32
	for frames := range result.Stream.Frames() {
		got = append(got, frames...)
	}
	if err := result.Stream.Err(); err != nil {
		t.Fatalf("stream error: %v", err)
	}
	if !result.Stream.Incremental() {
		t.Error("a streamed response produced a stream the player would still buffer whole")
	}
	want := []float32{0.1, 0.2, 0.3, 0.4, 0.5, 0.6}
	if len(got) != len(want) {
		t.Fatalf("read %d samples, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("sample %d = %v, want %v", i, got[i], want[i])
		}
	}

	_, requests := srv.seen()
	if len(requests) == 0 {
		t.Fatal("no request reached the speech worker")
	}
	if requests[0]["stream"] != true {
		t.Fatalf("synthesize request = %v, want stream:true", requests[0])
	}
}

// The same worker with streaming ruled out must produce the old shape: a
// stream the player buffers whole, and an explicit stream:false on the wire so
// the worker's own environment default cannot override the decision.
func TestRemoteProviderPinsStageAWhenStreamingIsOff(t *testing.T) {
	srv := startFakeSpeechServer(t, &fakeSpeechServer{
		token: "s3cret",
		ready: `{"type":"ready","protocol":"obey-speech/v1","sample_rate":24000,"pcm_format":"f32le",` +
			`"streaming":true,"streaming_capable":true,"tier":"0.6b"}`,
		chunks: [][]float32{{0.1, 0.2}, {0.3, 0.4}},
	})
	cfg := lanConfig(srv.url(), "s3cret")
	cfg.QwenTTSStream = config.QwenStreamOff

	remote, err := newRemoteQwen3TTS(cfg)
	if err != nil {
		t.Fatalf("newRemoteQwen3TTS: %v", err)
	}
	defer remote.Delete()
	if remote.WorkerStreaming() {
		t.Fatal("qwen_tts_stream=off still reported a streaming worker")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	result, err := remote.SynthesizeRequest(ctx, tts.SynthesisRequest{Text: "hello there"})
	if err != nil {
		t.Fatalf("SynthesizeRequest: %v", err)
	}
	for range result.Stream.Frames() {
	}
	if result.Stream.Incremental() {
		t.Error("streaming was off and the stream was still marked for incremental playback")
	}
	_, requests := srv.seen()
	if requests[0]["stream"] != false {
		t.Fatalf("synthesize request = %v, want an explicit stream:false", requests[0])
	}
}

// A worker that advertises only the older streaming flag is still capable:
// its default is computed as capability AND the environment flag, so it could
// not have said true without being able to.
func TestReadyStreamingImpliesCapability(t *testing.T) {
	if !(speechReadyMsg{Streaming: true}).streamCapable() {
		t.Error("streaming=true did not imply capability")
	}
	if !(speechReadyMsg{StreamingCapable: true}).streamCapable() {
		t.Error("streaming_capable=true was not read")
	}
	if (speechReadyMsg{}).streamCapable() {
		t.Error("a stage A ready read as capable")
	}
}

// stubSpeechSession fails its first synthesis with a restartable error and
// succeeds afterwards, so the provider's reconnect path can be driven without
// a process.
type stubSpeechSession struct {
	*speechSession
	fail  bool
	wrote int
}

func (s *stubSpeechSession) SynthesizeToStream(_ context.Context, _ tts.SynthesisRequest, stream *audio.PCMStream) error {
	if s.fail {
		return &tts.ProviderError{Provider: qwen3TTSProviderName, Operation: "synthesize", Kind: tts.ProviderErrorWorker, Err: errors.New("worker died")}
	}
	if err := stream.SetSampleRate(qwen3TTSSampleRate); err != nil {
		return err
	}
	s.wrote++
	return stream.Write([]float32{0.1, 0.2})
}

func (*stubSpeechSession) Close() {}

// The replacement worker answers the capability question for itself. Leaving
// the stream describing the session that just died is wrong in the direction
// that matters: marked incremental while a blob arrives, which would start
// playback on a partial buffer that is filled in one burst.
func TestReconnectRedeclaresIncrementalForTheReplacementSession(t *testing.T) {
	for _, tc := range []struct {
		name             string
		replacementLocal bool
		want             bool
	}{
		{name: "replacement cannot stream", replacementLocal: false, want: false},
		{name: "replacement can stream", replacementLocal: true, want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q := newQwen3TTS("", "", time.Minute, nil)
			q.native = true
			q.streamMode = config.QwenStreamOn
			q.alive.Store(true)

			// The dead session streamed; the replacement may not.
			dead := &stubSpeechSession{speechSession: &speechSession{label: nativeWorkerLabel}, fail: true}
			dead.capable = true
			dead.applyStreamPolicy(true)

			live := &stubSpeechSession{speechSession: &speechSession{label: nativeWorkerLabel}}
			live.capable = tc.replacementLocal

			handed := []qwenSpeechSession{dead, live}
			q.startSpeech = func(context.Context) (qwenSpeechSession, error) {
				next := handed[0]
				handed = handed[1:]
				return next, nil
			}

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			stream := audio.NewPCMStream(ctx)
			go func() {
				for range stream.Frames() {
				}
			}()
			if err := q.synthesizeNativeStream(ctx, tts.SynthesisRequest{Text: "hi"}, stream); err != nil {
				t.Fatalf("synthesizeNativeStream: %v", err)
			}
			stream.Close()

			if live.wrote != 1 {
				t.Fatalf("the replacement session synthesized %d times, want 1", live.wrote)
			}
			if got := stream.Incremental(); got != tc.want {
				t.Fatalf("stream.Incremental() = %v after reconnect, want %v: the flag still describes the dead session", got, tc.want)
			}
		})
	}
}

func TestNativeRequestWarmEvidenceTracksSessionLifecycle(t *testing.T) {
	q := newQwen3TTS("", "", time.Minute, nil)
	q.native = true
	q.alive.Store(true)
	q.startSpeech = func(context.Context) (qwenSpeechSession, error) {
		return &stubSpeechSession{speechSession: &speechSession{label: nativeWorkerLabel}}, nil
	}

	run := func(want string) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		stream := audio.NewPCMStream(ctx)
		go func() {
			for range stream.Frames() {
			}
		}()
		if err := q.synthesizeNativeStream(ctx, tts.SynthesisRequest{Text: "hi"}, stream); err != nil {
			t.Fatalf("synthesizeNativeStream: %v", err)
		}
		if got := stream.SynthesisWarmState(); got != want {
			t.Fatalf("SynthesisWarmState() = %q, want %q", got, want)
		}
		stream.Close()
	}

	run("cold")
	run("warm")
}

func TestNativeReconnectDowngradesWarmEvidenceToCold(t *testing.T) {
	q := newQwen3TTS("", "", time.Minute, nil)
	q.native = true
	q.alive.Store(true)
	dead := &stubSpeechSession{speechSession: &speechSession{label: nativeWorkerLabel}, fail: true}
	live := &stubSpeechSession{speechSession: &speechSession{label: nativeWorkerLabel}}
	q.speech = dead
	var current qwenSpeechSession = dead
	q.speechRef.Store(&current)
	q.speechCompleted.Store(true)
	q.startSpeech = func(context.Context) (qwenSpeechSession, error) { return live, nil }

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	stream := audio.NewPCMStream(ctx)
	go func() {
		for range stream.Frames() {
		}
	}()
	if err := q.synthesizeNativeStream(ctx, tts.SynthesisRequest{Text: "hi"}, stream); err != nil {
		t.Fatalf("synthesizeNativeStream: %v", err)
	}
	if got := stream.SynthesisWarmState(); got != "cold" {
		t.Fatalf("restarted request state = %q, want cold", got)
	}
	if !q.speechCompleted.Load() {
		t.Fatal("successful replacement request did not publish current-session success")
	}
	stream.Close()
}

func TestNativeCanceledRequestDoesNotPublishSuccessEvidence(t *testing.T) {
	q := newQwen3TTS("", "", time.Minute, nil)
	q.native = true
	q.alive.Store(true)
	q.speech = &stubSpeechSession{speechSession: &speechSession{label: nativeWorkerLabel}, fail: true}
	current := q.speech
	q.speechRef.Store(&current)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	stream := audio.NewPCMStream(ctx)
	if err := q.synthesizeNativeStream(ctx, tts.SynthesisRequest{Text: "hi"}, stream); err == nil {
		t.Fatal("canceled request unexpectedly succeeded")
	}
	if q.speechCompleted.Load() {
		t.Fatal("canceled request published success evidence")
	}
	if got := stream.SynthesisWarmState(); got != "cold" {
		t.Fatalf("canceled first request state = %q, want cold", got)
	}
	stream.Close()
}

// Nothing is retried once the listener has heard part of the sentence: a
// second pass would append a duplicate to the same stream.
func TestNoReconnectAfterSamplesHavePlayed(t *testing.T) {
	q := newQwen3TTS("", "", time.Minute, nil)
	q.native = true
	q.alive.Store(true)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	stream := audio.NewPCMStream(ctx)
	go func() {
		for range stream.Frames() {
		}
	}()

	starts := 0
	q.startSpeech = func(context.Context) (qwenSpeechSession, error) {
		starts++
		session := &partialThenFailSession{speechSession: &speechSession{label: nativeWorkerLabel}}
		return session, nil
	}

	err := q.synthesizeNativeStream(ctx, tts.SynthesisRequest{Text: "hi"}, stream)
	if err == nil {
		t.Fatal("a worker that died after playing audio was silently retried")
	}
	if !strings.Contains(err.Error(), "already playing") {
		t.Fatalf("error = %v, want it to name the audio already played", err)
	}
	if starts != 1 {
		t.Fatalf("started %d sessions, want 1: the sentence would have been spoken twice", starts)
	}
	stream.Close()
}

// partialThenFailSession writes part of an utterance and then dies, which is
// the shape a mid-stream worker death takes.
type partialThenFailSession struct{ *speechSession }

func (s *partialThenFailSession) SynthesizeToStream(_ context.Context, _ tts.SynthesisRequest, stream *audio.PCMStream) error {
	if err := stream.SetSampleRate(qwen3TTSSampleRate); err != nil {
		return err
	}
	if err := stream.Write([]float32{0.1, 0.2}); err != nil {
		return err
	}
	return &tts.ProviderError{Provider: qwen3TTSProviderName, Operation: "synthesize", Kind: tts.ProviderErrorWorker, Err: errors.New("worker died mid-utterance")}
}

func (*partialThenFailSession) Close() {}
