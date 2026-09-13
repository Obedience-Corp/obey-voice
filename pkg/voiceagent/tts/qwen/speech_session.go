package qwen

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	tts "github.com/Obedience-Corp/obey-voice/pkg/voiceagent/tts"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	managedqwen "github.com/Obedience-Corp/obey-voice/pkg/voiceagent/qwen"
)

// qwenSpeechSession is one warm Qwen speech transport: the local child process
// or a speech-serve connection. The provider drives both through this.
type qwenSpeechSession interface {
	SynthesizeToStream(ctx context.Context, req tts.SynthesisRequest, stream *audio.PCMStream) error
	CancelActive() error
	Presets() []string
	Streaming() bool
	Close()
}

// speechSession is the transport-independent half: request ids, one request in
// flight, and the synthesize / pcm_meta / final loop. Both transports frame
// identically after their own handshake, so this code is shared rather than
// written twice (spec/30 §3).
type speechSession struct {
	conn  *speechConn
	label string
	// referenceAudio is false where a local file path is meaningless to the
	// worker: obey-speech/v1 does not carry ref_wav in v1.
	referenceAudio bool

	mu       sync.Mutex
	closed   bool
	activeMu sync.Mutex
	activeID string
	request  uint64
	rate     int

	streaming bool
	capable   bool
	// requestStream is the host's decision for this transport, applied by the
	// provider once the handshake has said whether the worker can honour it.
	// Atomic because Capabilities() and the metrics path read it off the
	// synthesis goroutine.
	requestStream atomic.Bool
	presets       []string
}

// applyStreamPolicy records whether this session asks the worker for Stage B
// chunks. It is only meaningful on a capable worker: an incapable one ignores
// the request field, and telling it otherwise would make Capabilities() claim
// a behaviour the wire will not produce.
func (s *speechSession) applyStreamPolicy(stream bool) {
	s.requestStream.Store(stream && s.capable)
}

// StreamingCapable reports the worker's ready.streaming_capable.
func (s *speechSession) StreamingCapable() bool { return s != nil && s.capable }

// StreamRequested reports whether this session asks for chunks, which is what
// decides whether the PCM it produces can be played before synthesis ends.
func (s *speechSession) StreamRequested() bool { return s != nil && s.requestStream.Load() }

func (s *speechSession) Presets() []string {
	if s == nil {
		return nil
	}
	return s.presets
}

func (s *speechSession) Streaming() bool { return s != nil && s.streaming }

// beginClose marks the session closed exactly once. It waits for an in-flight
// synthesis, as Close always has.
func (s *speechSession) beginClose() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false
	}
	s.closed = true
	return true
}

// SynthesizeToStream runs one request and writes float32 PCM into stream.
func (s *speechSession) SynthesizeToStream(ctx context.Context, req tts.SynthesisRequest, stream *audio.PCMStream) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return fmt.Errorf("%s is closed", s.label)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	s.request++
	id := fmt.Sprintf("nqwen-%d", s.request)

	msg := map[string]any{
		"type": "synthesize",
		"id":   id,
		"text": req.Text,
	}
	// Only a capable worker is told anything about streaming, so the request a
	// Stage A worker reads is byte-identical to the one it has always read.
	// A capable one is told explicitly in both directions, never by omission:
	// its own default comes from QWEN3_TTS_STREAM in whatever environment
	// started it, so leaving the field out on a Metal box would stream exactly
	// where this host has decided it must not.
	if s.capable {
		msg["stream"] = s.requestStream.Load()
	}
	if ref := strings.TrimSpace(req.ReferenceAudio); ref != "" && s.referenceAudio {
		msg["ref_wav"] = ref
	}
	// Shipped clones are not model presets. A ref_wav is the throat.
	if _, shipped := managedqwen.LookupShipped(req.Voice); !shipped {
		if preset := strings.TrimSpace(req.Voice); preset != "" && !strings.EqualFold(preset, "default") {
			msg["preset"] = preset
		}
	}
	// The active id is published under the write lock so a concurrent
	// CancelActive cannot name a request the worker has not read yet.
	var aborted error
	err := s.conn.WriteJSONFunc(func() (any, error) {
		if aborted = ctx.Err(); aborted != nil {
			return nil, aborted
		}
		s.activeMu.Lock()
		s.activeID = id
		s.activeMu.Unlock()
		return msg, nil
	})
	if aborted != nil {
		return aborted
	}
	if err != nil {
		s.clearActive(id)
		return fmt.Errorf("write %s request: %w", s.label, err)
	}
	defer s.clearActive(id)

	return s.readSynthesis(ctx, id, stream)
}

// readSynthesis consumes control lines until final, writing PCM as it arrives.
// Stage A sends one pcm_meta, Stage B several; this loop concatenates either.
//
// A Stage B worker numbers its chunks and totals them on final precisely so a
// reader can tell a complete stream from a truncated one. Those fields are
// checked here, not discarded: without them a dropped chunk arrives as audio
// that is simply shorter than what was said, which is the one failure a
// listener cannot detect and the pipeline would score as a healthy turn.
func (s *speechSession) readSynthesis(ctx context.Context, id string, stream *audio.PCMStream) error {
	rateSet := false
	var tally chunkTally
	for {
		if err := ctx.Err(); err != nil {
			_ = s.sendCancel(id)
			return err
		}
		line, err := s.conn.ReadLine(ctx)
		if err != nil {
			return err
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var head struct {
			Type       string `json:"type"`
			ID         string `json:"id"`
			Message    string `json:"message"`
			SampleRate int    `json:"sample_rate"`
			NSamples   int    `json:"n_samples"`
			// Chunk is this frame's 0-based index within a streamed response.
			// A pointer because absent (Stage A) and 0 (the first streamed
			// chunk) are different facts.
			Chunk *int `json:"chunk"`
			// Chunks is the total the worker claims to have sent, present only
			// on a streamed final.
			Chunks int `json:"chunks"`
		}
		if err := json.Unmarshal([]byte(line), &head); err != nil {
			return fmt.Errorf("%s line: %w (%q)", s.label, err, line)
		}
		switch head.Type {
		case "error":
			return fmt.Errorf("%s error: %s", s.label, head.Message)
		case "pcm_meta":
			if err := tally.accept(s.label, head.Chunk, head.NSamples); err != nil {
				return err
			}
			if err := s.readPCMChunk(ctx, id, head.NSamples, head.SampleRate, &rateSet, stream); err != nil {
				return err
			}
		case "final":
			if err := tally.verify(s.label, head.Chunks, head.NSamples); err != nil {
				return err
			}
			if !rateSet {
				if err := stream.SetSampleRate(qwen3TTSSampleRate); err != nil {
					return err
				}
			}
			return nil
		case "generating":
			// progress only
		default:
			// ignore future control types
		}
	}
}

// chunkTally counts what a response actually delivered so the worker's own
// totals can be checked against it.
type chunkTally struct {
	frames   int
	samples  int
	indexed  bool
	nextIdx  int
	numbered int
}

// accept records one pcm_meta and rejects a gap in the chunk numbering.
//
// Out-of-order or missing indices are the shape a lost frame takes on a
// byte-stream transport: the payload lengths still line up, so nothing else in
// the reader would notice.
func (t *chunkTally) accept(label string, index *int, samples int) error {
	t.frames++
	t.samples += samples
	if index == nil {
		return nil
	}
	t.indexed = true
	t.numbered++
	if *index != t.nextIdx {
		return fmt.Errorf("%s pcm chunk %d arrived where %d was expected; a chunk was dropped or reordered", label, *index, t.nextIdx)
	}
	t.nextIdx++
	return nil
}

// verify checks a streamed final's totals against what was read.
//
// Stage A sends neither field and is not checked — there is nothing to check,
// one blob either arrives or the read fails. A response that carried numbered
// chunks but ends without totals is a different matter: something between the
// worker and here dropped the fields that make truncation detectable, and
// accepting it would be accepting exactly the silence this guards against.
func (t *chunkTally) verify(label string, chunks, samples int) error {
	if chunks <= 0 {
		if t.indexed {
			return fmt.Errorf("%s streamed %d chunks but final carried no chunk total", label, t.numbered)
		}
		return nil
	}
	if chunks != t.frames {
		return fmt.Errorf("%s final reports %d chunks, host read %d", label, chunks, t.frames)
	}
	if samples > 0 && samples != t.samples {
		return fmt.Errorf("%s final reports %d samples, host read %d", label, samples, t.samples)
	}
	return nil
}

func (s *speechSession) readPCMChunk(ctx context.Context, id string, n, metaRate int, rateSet *bool, stream *audio.PCMStream) error {
	if n <= 0 {
		return fmt.Errorf("%s pcm_meta n_samples=%d", s.label, n)
	}
	rate := metaRate
	if rate == 0 {
		rate = s.rate
	}
	if rate != qwen3TTSSampleRate {
		return fmt.Errorf("%s returned %d Hz, want %d Hz", s.label, rate, qwen3TTSSampleRate)
	}
	if !*rateSet {
		if err := stream.SetSampleRate(rate); err != nil {
			return err
		}
		*rateSet = true
	}
	samples, err := s.conn.ReadPCM(n)
	if err != nil {
		return fmt.Errorf("%s pcm read: %w", s.label, err)
	}
	const chunk = 2048
	for start := 0; start < len(samples); start += chunk {
		if err := ctx.Err(); err != nil {
			_ = s.sendCancel(id)
			return err
		}
		end := min(start+chunk, len(samples))
		if err := stream.Write(samples[start:end]); err != nil {
			return err
		}
	}
	return nil
}

func (s *speechSession) sendCancel(id string) error {
	return s.conn.WriteJSON(map[string]string{"type": "cancel", "id": id})
}

// CancelActive writes a cancellation for the session-generated request ID
// without waiting for the synthesis/read lock. Stage A workers may only act on
// it between requests, but the write remains prompt and Stage B can interrupt
// mid-synthesis.
func (s *speechSession) CancelActive() error {
	if s == nil {
		return nil
	}
	// The id is read under the write lock: a synthesize that has published its
	// id but not yet written the line must be waited out, not cancelled blind.
	err := s.conn.WriteJSONFunc(func() (any, error) {
		s.activeMu.Lock()
		id := s.activeID
		s.activeMu.Unlock()
		if id == "" {
			return nil, errNoActiveRequest
		}
		return map[string]string{"type": "cancel", "id": id}, nil
	})
	if errors.Is(err, errNoActiveRequest) {
		return nil
	}
	return err
}

func (s *speechSession) clearActive(id string) {
	s.activeMu.Lock()
	defer s.activeMu.Unlock()
	if s.activeID == id {
		s.activeID = ""
	}
}
