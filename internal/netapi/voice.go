package netapi

import (
	"encoding/base64"
	"encoding/binary"
	"errors"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

// Remote push-to-talk and client-driven barge-in: who owns the mic, when the
// claim is taken, and what a cancelled turn tells every client. Split out of
// stream.go, which owns the socket and the fan-out.

// setIngress wires the remote audio ingress used by push-to-talk turns.
func (h *hub) setIngress(ing *audio.Ingress) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.ingress = ing
}

func (h *hub) claimMic(c *streamConn) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.ingress == nil {
		return errors.New("remote mic is not enabled on this serve instance")
	}
	// A claim is only ever granted to a live connection. This is what stops a
	// voice turn queued by a client that has since disconnected from taking
	// the mic when it is finally dequeued.
	if _, ok := h.conns[c]; !ok {
		return errors.New("connection is closed")
	}
	if h.micClaimant != nil && h.micClaimant != c {
		return errors.New("microphone claimed by another client")
	}
	h.micClaimant = c
	h.ingress.Reset()
	return nil
}

// isClaimant reports whether c currently owns the exclusive remote mic.
func (h *hub) isClaimant(c *streamConn) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.micClaimant == c
}

func (h *hub) releaseMic(c *streamConn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.micClaimant == c {
		h.micClaimant = nil
		// If the client disconnects mid-utterance, finalize so STT is not
		// left blocked waiting for more frames.
		if h.ingress != nil {
			h.ingress.Finalize()
		}
	}
}

func (h *hub) writeMic(c *streamConn, samples []float32) error {
	h.mu.Lock()
	ing := h.ingress
	claim := h.micClaimant
	h.mu.Unlock()
	if ing == nil {
		return errors.New("remote mic is not enabled")
	}
	if claim != c {
		return errors.New("microphone not claimed by this client")
	}
	return ing.Write(samples)
}

func (h *hub) endMicUtterance(c *streamConn) error {
	h.mu.Lock()
	ing := h.ingress
	claim := h.micClaimant
	h.mu.Unlock()
	if ing == nil {
		return errors.New("remote mic is not enabled")
	}
	if claim != c {
		return errors.New("microphone not claimed by this client")
	}
	ing.Finalize()
	return nil
}

// startVoiceTurn claims the exclusive mic and enqueues one push-to-talk turn
// (Phase 4 / WI-62e19b).
//
// The claim is taken here only while the pipeline is idle — ordinary
// push-to-talk, and the interrupt → voice_start barge-in — so the client may
// stream PCM as soon as it has sent voice_start. A voice_start that lands
// while another turn is in flight is still queued, but claims nothing until it
// is dequeued: a false trigger, or a client that changes its mind, must not
// hold the mic away from whoever is actually talking.
func (s *Server) startVoiceTurn(conn *streamConn) error {
	deferred := s.dispatcher.Pending()
	prepare := func() bool { return true }
	if deferred {
		conn.voiceQueued.Store(true)
		prepare = func() bool { return s.claimQueuedMic(conn) }
	} else if err := s.hub.claimMic(conn); err != nil {
		return err
	}

	if err := s.dispatcher.SubmitVoice(prepare); err != nil {
		conn.voiceQueued.Store(false)
		s.hub.releaseMic(conn)
		return errors.New(dispatchErrText(err))
	}
	return nil
}

// claimQueuedMic runs on the dispatch goroutine when a queued voice turn
// reaches the front of the queue: the one place a deferred voice_start takes
// the mic. It abandons the turn rather than run it when the client is gone,
// gave up, or lost the race for the mic to another client.
func (s *Server) claimQueuedMic(conn *streamConn) bool {
	if !conn.voiceQueued.Swap(false) {
		return false
	}
	if err := s.hub.claimMic(conn); err != nil {
		s.sendError(conn, err.Error())
		return false
	}
	return true
}

// endVoiceTurn finalizes the utterance and drops the exclusive claim, so
// another client can talk while TTS plays (releaseMic finalizes again,
// idempotently).
//
// A voice_end from a client whose turn is still queued abandons that turn
// instead: it never held the mic, so no audio was accepted for it, and letting
// it dequeue would claim the mic for a turn nobody is speaking into.
func (s *Server) endVoiceTurn(conn *streamConn) error {
	if !s.hub.isClaimant(conn) {
		if conn.voiceQueued.Swap(false) {
			return errors.New("voice turn was queued behind another turn and never claimed the mic; send voice_start again")
		}
		return errors.New("microphone not claimed by this client")
	}
	if err := s.hub.endMicUtterance(conn); err != nil {
		return err
	}
	s.hub.releaseMic(conn)
	return nil
}

// remoteInterruptReason labels the two events a client-driven barge-in emits,
// distinguishing it in a transcript from the host mic's local "barge-in".
const remoteInterruptReason = "remote_interrupt"

// interrupt cancels the in-flight turn and tells every client what was
// canceled. audio_reset alone said "drop the audio you have buffered" but
// never "that turn is over", so a client had to infer its own turn state from
// turn_metrics. Now the same two events the local barge-in path emits also
// come out of a remote interrupt: turn_interrupted always, and
// speaking_interrupted as well when TTS was in flight.
//
// An interrupt that hits an idle server still resets audio — it is the client
// saying "I am about to talk" — but emits neither event: nothing was
// interrupted, and a client must not be told a turn ended that never began.
func (s *Server) interrupt() {
	// Read what is in flight before canceling; both flags fall the moment
	// the turn unwinds.
	speaking := s.hub.speaking.Load()
	interrupted := s.dispatcher.TurnActive() || speaking

	s.hub.resetAudio()
	s.dispatcher.Interrupt()

	if !interrupted {
		return
	}
	if speaking {
		s.opts.Bus.Emit(events.SpeakingInterrupted{Reason: remoteInterruptReason})
	}
	s.opts.Bus.Emit(events.TurnInterrupted{Reason: remoteInterruptReason})
}

// decodePCMS16LE decodes base64 little-endian mono PCM into float32 samples
// at the capture sample rate. Non-16 kHz input is rejected (client resamples).
func decodePCMS16LE(b64 string, sampleRate int) ([]float32, error) {
	if b64 == "" {
		return nil, errors.New("empty data")
	}
	if sampleRate != 0 && sampleRate != audio.SampleRate {
		return nil, errors.New("sample_rate must be 16000 (resample on the client)")
	}
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil, errors.New("invalid base64")
	}
	if len(raw)%2 != 0 {
		return nil, errors.New("odd pcm byte length")
	}
	n := len(raw) / 2
	out := make([]float32, n)
	for i := 0; i < n; i++ {
		s := int16(binary.LittleEndian.Uint16(raw[i*2:]))
		out[i] = float32(s) / 32768
	}
	return out, nil
}
