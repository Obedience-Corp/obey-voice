package qwen

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"math"
	"strings"
	"testing"
	"time"
)

// nopCloser lets a test conn own a buffer the test still reads.
type nopCloser struct{ io.Writer }

func (nopCloser) Close() error { return nil }

func testConn(t *testing.T, in string) (*speechConn, *bytes.Buffer) {
	t.Helper()
	out := &bytes.Buffer{}
	return newSpeechConn(nativeWorkerLabel, nativeWorkerProtocol, strings.NewReader(in), out, nopCloser{out}), out
}

func pcmBytes(samples ...float32) string {
	raw := make([]byte, len(samples)*4)
	for i, s := range samples {
		binary.LittleEndian.PutUint32(raw[i*4:], math.Float32bits(s))
	}
	return string(raw)
}

func TestSpeechConnWriteJSONFramesOneLine(t *testing.T) {
	conn, out := testConn(t, "")

	if err := conn.WriteJSON(map[string]string{"type": "cancel", "id": "nqwen-1"}); err != nil {
		t.Fatalf("WriteJSON: %v", err)
	}
	if err := conn.WriteJSON(map[string]string{"type": "cancel", "id": "nqwen-2"}); err != nil {
		t.Fatalf("WriteJSON: %v", err)
	}
	want := `{"id":"nqwen-1","type":"cancel"}` + "\n" + `{"id":"nqwen-2","type":"cancel"}` + "\n"
	if got := out.String(); got != want {
		t.Fatalf("wire=%q want %q", got, want)
	}
}

func TestSpeechConnWriteJSONFuncAbortsOnBuildError(t *testing.T) {
	conn, out := testConn(t, "")
	sentinel := errors.New("nothing in flight")

	err := conn.WriteJSONFunc(func() (any, error) { return nil, sentinel })
	if !errors.Is(err, sentinel) {
		t.Fatalf("err=%v want %v", err, sentinel)
	}
	if out.Len() != 0 {
		t.Fatalf("aborted build wrote %q", out.String())
	}
}

func TestSpeechConnCloseSendsTheShutdownLine(t *testing.T) {
	conn, out := testConn(t, "")

	if err := conn.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// Byte-for-byte what the child worker has always been closed with.
	if got, want := out.String(), `{"type":"shutdown"}`+"\n"; got != want {
		t.Fatalf("shutdown=%q want %q", got, want)
	}
}

func TestSpeechConnReadLineErrors(t *testing.T) {
	// A pipe with no writer blocks forever, so the error cases cannot race a
	// spurious EOF.
	for _, tc := range []struct {
		name    string
		cancel  bool
		abort   error
		abortFn func(error) error
		want    string
	}{
		{name: "cancelled context", cancel: true, want: context.Canceled.Error()},
		{name: "transport died", abort: errors.New("signal: killed"), want: "native worker exited: signal: killed"},
		{
			name:    "transport death rendered by the owner",
			abort:   errors.New("signal: killed"),
			abortFn: func(err error) error { return errors.New("child gone: " + err.Error()) },
			want:    "child gone: signal: killed",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pr, pw := io.Pipe()
			defer pw.Close()
			conn := newSpeechConn(nativeWorkerLabel, nativeWorkerProtocol, pr, &bytes.Buffer{}, nil)
			if tc.abort != nil {
				abort := make(chan error, 1)
				abort <- tc.abort
				conn.abort = abort
				conn.abortErr = tc.abortFn
			}
			ctx := context.Background()
			if tc.cancel {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}

			if _, err := conn.ReadLine(ctx); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err=%v want %q", err, tc.want)
			}
		})
	}
}

func TestSpeechConnReadLineReturnsTheLine(t *testing.T) {
	const line = `{"type":"final","id":"nqwen-1"}` + "\n"
	conn, _ := testConn(t, line)

	got, err := conn.ReadLine(context.Background())
	if err != nil {
		t.Fatalf("ReadLine: %v", err)
	}
	if got != line {
		t.Fatalf("line=%q want %q", got, line)
	}
}

func TestSpeechConnReadPCMRoundTrip(t *testing.T) {
	want := []float32{0.5, -0.5, 0.25}
	for _, tc := range []struct {
		name string
		in   string
	}{
		{name: "stage A trailing newline", in: pcmBytes(want...) + "\n" + `{"type":"final"}` + "\n"},
		{name: "no trailing newline", in: pcmBytes(want...) + `{"type":"final"}` + "\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			conn, _ := testConn(t, tc.in)

			got, err := conn.ReadPCM(len(want))
			if err != nil {
				t.Fatalf("ReadPCM: %v", err)
			}
			for i := range want {
				if math.Abs(float64(got[i]-want[i])) > 1e-6 {
					t.Fatalf("sample[%d]=%v want %v", i, got[i], want[i])
				}
			}
			// The next line must still be readable: PCM framing may not eat it.
			line, err := conn.ReadLine(context.Background())
			if err != nil {
				t.Fatalf("ReadLine after PCM: %v", err)
			}
			if !strings.Contains(line, "final") {
				t.Fatalf("next line=%q", line)
			}
		})
	}
}

func TestSpeechConnReadPCMShortRead(t *testing.T) {
	conn, _ := testConn(t, pcmBytes(0.5))

	if _, err := conn.ReadPCM(3); err == nil {
		t.Fatal("expected a short PCM read to fail")
	}
	if _, err := conn.ReadPCM(0); err == nil {
		t.Fatal("expected n_samples=0 to fail")
	}
}

func TestSpeechConnValidateReady(t *testing.T) {
	const local = `{"type":"ready","protocol":"qwen3-tts-worker/v1","sample_rate":24000,"pcm_format":"f32le","streaming":false,"presets":["Vivian"],"note":"stage_A"}`
	for _, tc := range []struct {
		name  string
		proto string
		line  string
		want  string
	}{
		{name: "not ready", proto: nativeWorkerProtocol, line: `{"type":"error","message":"boom"}`, want: "handshake failed"},
		{name: "wrong protocol", proto: nativeWorkerProtocol, line: `{"type":"ready","protocol":"obey-speech/v1","sample_rate":24000,"pcm_format":"f32le"}`, want: "protocol"},
		{name: "wrong sample rate", proto: nativeWorkerProtocol, line: `{"type":"ready","protocol":"qwen3-tts-worker/v1","sample_rate":16000,"pcm_format":"f32le"}`, want: "sample rate"},
		{name: "wrong pcm format", proto: nativeWorkerProtocol, line: `{"type":"ready","protocol":"qwen3-tts-worker/v1","sample_rate":24000,"pcm_format":"s16le"}`, want: "PCM format"},
		{name: "local ok", proto: nativeWorkerProtocol, line: local, want: ""},
		{name: "lan protocol is a parameter", proto: speechProtocol, line: `{"type":"ready","protocol":"obey-speech/v1","sample_rate":24000,"pcm_format":"f32le","tier":"0.6b"}`, want: ""},
		{name: "lan rejects the child protocol", proto: speechProtocol, line: local, want: "protocol"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			conn := newSpeechConn(nativeWorkerLabel, tc.proto, strings.NewReader(""), &bytes.Buffer{}, nil)
			ready, err := conn.ParseReady(tc.line)
			if err != nil {
				t.Fatalf("ParseReady: %v", err)
			}
			err = conn.ValidateReady(ready)
			if tc.want == "" {
				if err != nil {
					t.Fatalf("ValidateReady: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err=%v want %q", err, tc.want)
			}
		})
	}
}

func TestSpeechConnParseReadyKeepsOptionalFieldsAndIgnoresUnknown(t *testing.T) {
	conn, _ := testConn(t, "")

	ready, err := conn.ParseReady(`{"type":"ready","protocol":"obey-speech/v1","sample_rate":24000,` +
		`"pcm_format":"f32le","streaming":true,"presets":["Vivian"],"note":"n","tier":"1.7b","future":42}`)
	if err != nil {
		t.Fatalf("ParseReady: %v", err)
	}
	if !ready.Streaming || ready.Note != "n" || ready.Tier != "1.7b" || len(ready.Presets) != 1 {
		t.Fatalf("ready=%+v", ready)
	}

	if _, err := conn.ParseReady("not json\n"); err == nil {
		t.Fatal("expected a malformed ready line to fail")
	}
}

func TestSpeechConnReadLineDoesNotOutliveDeadline(t *testing.T) {
	// A pipe with no writer blocks forever; the deadline must still return.
	pr, pw := io.Pipe()
	defer pw.Close()
	conn := newSpeechConn(nativeWorkerLabel, nativeWorkerProtocol, pr, &bytes.Buffer{}, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := conn.ReadLine(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err=%v want deadline exceeded", err)
	}
}
