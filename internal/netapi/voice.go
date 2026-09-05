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

// claimMic grants the exclusive mic to the turn gen identifies. The generation
// is recorded with the claim, under the same lock, so a deadline that fires for
// an earlier turn cannot release what a later one was just granted.
func (h *hub) claimMic(c *streamConn, gen uint64) error {
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
	h.micClaimant, h.micGen = c, gen
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
	h.releaseLocked(c)
}

// releaseTurnMic takes the mic back for the turn gen identifies, and reports
// whether the recogniser was still waiting on that turn's audio. A claim the
// connection was granted for a *later* turn is left alone: the deadline that
// calls this can be firing for a turn that ended while the next one was being
// dequeued, and releasing then would cut off whoever is talking now.
func (h *hub) releaseTurnMic(c *streamConn, gen uint64) (utteranceOpen bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.micGen != gen {
		return false
	}
	return h.releaseLocked(c)
}

// releaseLocked drops c's claim and reports whether the recogniser was still
// waiting on its audio — a claim whose utterance is already closed belongs to a
// turn that has moved past STT. Callers hold h.mu.
func (h *hub) releaseLocked(c *streamConn) (utteranceOpen bool) {
	if h.micClaimant != c {
		return false
	}
	utteranceOpen = h.ingress != nil && !h.ingress.Finalized()
	h.micClaimant, h.micGen = nil, 0
	// If the client disconnects mid-utterance, finalize so STT is not left
	// blocked waiting for more frames.
	if h.ingress != nil {
		h.ingress.Finalize()
	}
	return utteranceOpen
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
	// The turn is open from here: only voice_end, a disconnect, or this
	// deadline will close it again. It is armed before anything is claimed or
	// staged because its generation is the turn's identity, and the claim, the
	// staging buffer and the queued marker all record it.
	gen := s.armVoiceTurn(conn)
	prepare := func() bool { return true }
	if deferred {
		conn.openQueuedTurn(gen)
		prepare = func() bool { return s.claimQueuedMic(conn) }
	} else if err := s.hub.claimMic(conn, gen); err != nil {
		// No turn was opened, and no live claim can be stranded by disarming:
		// a connection that already held one would have been granted this
		// claim too.
		conn.watch.disarm()
		return err
	}

	if err := s.dispatcher.SubmitVoice(prepare); err != nil {
		conn.dropStaged()
		conn.watch.disarm()
		if !deferred {
			// Release only what this call claimed. A deferred voice_start
			// claimed nothing, and this connection may still be speaking into
			// a live claim from an earlier turn.
			s.hub.releaseMic(conn)
		}
		return err
	}
	return nil
}

// writeVoiceAudio routes one inbound PCM frame: into the shared ingress when
// this connection owns the mic, into its own staging buffer while its turn is
// still queued. A queued client keeps streaming exactly as it always has; the
// frames simply wait with it.
func (s *Server) writeVoiceAudio(conn *streamConn, samples []float32) error {
	// A turn that is being fed is a turn its client has not abandoned,
	// whether the frames are landing in the ingress or in staging.
	conn.watch.refresh()
	if conn.stage(samples) {
		return nil
	}
	return s.hub.writeMic(conn, samples)
}

// claimQueuedMic runs on the dispatch goroutine when a queued voice turn
// reaches the front of the queue: the one place a deferred voice_start takes
// the mic. It abandons the turn rather than run it when the client is gone,
// gave up, or lost the race for the mic to another client.
func (s *Server) claimQueuedMic(conn *streamConn) bool {
	// The turn is taken whole, and first: its marker and its audio leave the
	// connection in one step, so the deadline armed for it can only find it
	// already gone. Taken apart, a stall landing between the two emptied the
	// buffer this replay is about to write and said nothing to anyone.
	staged, final, ok := conn.takeQueuedTurn()
	if !ok {
		conn.watch.disarm()
		return false
	}
	// The turn starts here, so its deadline does too: a client is not charged
	// for the time the pipeline kept it waiting. Re-arming also retires the
	// deadline the queued turn was waiting under, and the fresh generation is
	// what the claim below records.
	gen := s.armVoiceTurn(conn)
	if err := s.hub.claimMic(conn, gen); err != nil {
		conn.watch.disarm()
		s.sendError(conn, err.Error())
		return false
	}

	// claimMic just reset the ingress, so replay what the client streamed
	// while it waited before any live frame can reach it.
	if len(staged) > 0 {
		if err := s.hub.writeMic(conn, staged); err != nil {
			s.sendError(conn, err.Error())
		}
	}
	if final {
		// The client finished speaking while it waited. Finalize and hand the
		// mic straight back, exactly as a live voice_end would have.
		if err := s.hub.endMicUtterance(conn); err != nil {
			s.sendError(conn, err.Error())
		}
		s.hub.releaseMic(conn)
		conn.watch.disarm()
		return true
	}
	return true
}

// endVoiceTurn finalizes the utterance and drops the exclusive claim, so
// another client can talk while TTS plays (releaseMic finalizes again,
// idempotently).
//
// A voice_end for a turn that is still queued records the end of the utterance
// against the staged audio instead; the turn finalizes itself the moment it is
// dequeued. The client therefore sees the same result whether or not the
// pipeline happened to be busy when it started talking.
func (s *Server) endVoiceTurn(conn *streamConn) error {
	if !s.hub.isClaimant(conn) && conn.markStagedFinal() {
		// The client is done talking; the dequeue finalizes for it.
		conn.watch.disarm()
		return nil
	}
	// Re-checked, not repeated by accident: the claim can be granted between
	// the two calls, and then this is an ordinary voice_end after all.
	if !s.hub.isClaimant(conn) {
		return errors.New("microphone not claimed by this client")
	}
	// Anything this connection queued behind its own live turn has no audio
	// of its own; drop it rather than let it dequeue and take the mic. The
	// immediate-claim path stages nothing, so a fast tap is unaffected.
	conn.dropStaged()
	if err := s.hub.endMicUtterance(conn); err != nil {
		return err
	}
	s.hub.releaseMic(conn)
	conn.watch.disarm()
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
	live := s.dispatcher.TurnActive() && !s.dispatcher.Interrupted()
	interrupted := live || speaking

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
