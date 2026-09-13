package qwen

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"
	"sync"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

// speechConn is the line/PCM framing shared by every Qwen speech transport.
// The local child speaks qwen3-tts-worker/v1 over stdio; the LAN client speaks
// obey-speech/v1 over TCP. Both frame the same way — one JSON line per message,
// raw f32le PCM after a pcm_meta — so the expected protocol and the error label
// are parameters, not constants.
type speechConn struct {
	label string // error prefix, e.g. "native worker"
	proto string // ready.protocol this transport requires

	r      *bufio.Reader
	w      io.Writer
	closer io.Closer

	writeMu sync.Mutex

	// abort reports that the transport died (child exit, socket close) so a
	// blocked ReadLine fails instead of hanging. abortErr renders that cause;
	// the owner supplies it because only the owner has the diagnostics.
	abort    <-chan error
	abortErr func(error) error
}

// speechProtocol is the LAN wire contract (spec/30 obey-speech/v1). speech-serve
// rewrites the child's protocol to this; the child itself never sees it. config
// owns the string because the doctor probe speaks it too.
const speechProtocol = config.SpeechProtocol

// shutdownLine is the exact byte sequence the child worker has always been
// closed with. It is a literal rather than a marshalled map so the extraction
// cannot change one byte of what the worker reads.
const shutdownLine = `{"type":"shutdown"}` + "\n"

// errNotReady marks a handshake whose first line was not a ready message. The
// caller adds captured worker output to that case only.
var errNotReady = errors.New("handshake failed")

// errNoActiveRequest aborts a WriteJSONFunc build when there is nothing to name.
var errNoActiveRequest = errors.New("no active request")

type speechReadyMsg struct {
	Type       string `json:"type"`
	Protocol   string `json:"protocol"`
	SampleRate int    `json:"sample_rate"`
	PCMFormat  string `json:"pcm_format"`
	// Streaming is the worker's own default for a request that does not say.
	Streaming bool `json:"streaming"`
	// StreamingCapable is whether the worker was built against an engine with
	// a PCM callback at all. It is the field that decides whether asking for
	// chunks means anything: a worker can be capable and still default to
	// whole-utterance, and a worker that is not capable ignores the request
	// field entirely. Absent (every Stage A worker in the field today) decodes
	// as false, which is the truth about them.
	StreamingCapable bool     `json:"streaming_capable"`
	Presets          []string `json:"presets,omitempty"`
	Note             string   `json:"note,omitempty"`
	// Tier is optional and speech-serve only; the child worker never emits it
	// and no path may require it.
	Tier string `json:"tier,omitempty"`
}

// streamCapable reports whether this worker can emit PCM during synthesis.
//
// streaming_capable is the direct answer, but a worker that reports
// streaming=true has already answered it: its default is computed as
// capability AND the environment flag, so it cannot default to streaming
// without being capable. Reading both keeps a worker that advertises only the
// older field from being told to speak whole utterances it was built to
// stream.
func (r speechReadyMsg) streamCapable() bool {
	return r.StreamingCapable || r.Streaming
}

func newSpeechConn(label, proto string, r io.Reader, w io.Writer, closer io.Closer) *speechConn {
	return &speechConn{label: label, proto: proto, r: bufio.NewReader(r), w: w, closer: closer}
}

// WriteJSON marshals v and writes it as one newline-terminated line.
func (c *speechConn) WriteJSON(v any) error {
	return c.WriteJSONFunc(func() (any, error) { return v, nil })
}

// WriteJSONFunc builds the message under the write lock. Callers whose message
// names state another writer may change (the active request id) use it so the
// state and the line describing it cannot be reordered. A build error aborts
// the write and is returned unwrapped.
func (c *speechConn) WriteJSONFunc(build func() (any, error)) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	v, err := build()
	if err != nil {
		return err
	}
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = c.w.Write(append(data, '\n'))
	return err
}

// ReadLineBlocking reads one line with no cancellation. Startup handshakes own
// their own timeout and exit races, so they read this way.
func (c *speechConn) ReadLineBlocking() (string, error) {
	return c.r.ReadString('\n')
}

// ReadLine reads one line, failing on ctx cancellation or transport death.
func (c *speechConn) ReadLine(ctx context.Context) (string, error) {
	type result struct {
		line string
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		line, err := c.r.ReadString('\n')
		ch <- result{line, err}
	}()
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case res := <-ch:
		return res.line, res.err
	case err := <-c.abort:
		if c.abortErr != nil {
			return "", c.abortErr(err)
		}
		return "", fmt.Errorf("%s exited: %v", c.label, err)
	}
}

// ReadPCM reads n little-endian float32 samples that follow a pcm_meta, then
// consumes the Stage A trailing newline if one is present.
func (c *speechConn) ReadPCM(n int) ([]float32, error) {
	if n <= 0 {
		return nil, fmt.Errorf("%s pcm n_samples=%d", c.label, n)
	}
	buf := make([]byte, n*4)
	if _, err := io.ReadFull(c.r, buf); err != nil {
		return nil, err
	}
	if b, err := c.r.ReadByte(); err == nil && b != '\n' {
		_ = c.r.UnreadByte()
	}
	samples := make([]float32, n)
	for i := range samples {
		samples[i] = math.Float32frombits(binary.LittleEndian.Uint32(buf[i*4:]))
	}
	return samples, nil
}

// ParseReady decodes a ready line. Unknown fields are ignored so a newer
// speech-serve cannot break an older host.
func (c *speechConn) ParseReady(line string) (speechReadyMsg, error) {
	var ready speechReadyMsg
	if err := json.Unmarshal([]byte(line), &ready); err != nil {
		return speechReadyMsg{}, fmt.Errorf("ready json: %w (%q)", err, strings.TrimSpace(line))
	}
	return ready, nil
}

// ValidateReady fails closed on anything this host cannot decode. The type
// mismatch wraps errNotReady because only that case carries worker output.
func (c *speechConn) ValidateReady(ready speechReadyMsg) error {
	if ready.Type != "ready" {
		return fmt.Errorf("%s %w: type=%s", c.label, errNotReady, ready.Type)
	}
	if ready.Protocol != c.proto {
		return fmt.Errorf("%s protocol %q unsupported; want %q", c.label, ready.Protocol, c.proto)
	}
	if ready.SampleRate != qwen3TTSSampleRate {
		return fmt.Errorf("%s sample rate %d unsupported; want %d", c.label, ready.SampleRate, qwen3TTSSampleRate)
	}
	if ready.PCMFormat != "f32le" {
		return fmt.Errorf("%s PCM format %q unsupported; want f32le", c.label, ready.PCMFormat)
	}
	return nil
}

// Close sends the shutdown line and closes the transport. Callers own whatever
// lives behind it (a child process, a socket).
func (c *speechConn) Close() error {
	c.writeMu.Lock()
	_, _ = c.w.Write([]byte(shutdownLine))
	c.writeMu.Unlock()
	if c.closer == nil {
		return nil
	}
	return c.closer.Close()
}
