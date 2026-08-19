package netapi

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/coder/websocket"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

// connQueueDepth bounds each connection's outbound *event* queue. Bus.Emit runs
// handlers synchronously on pipeline goroutines, so enqueue is non-blocking:
// a client that can't keep up on events is disconnected rather than allowed
// to stall a live turn.
const connQueueDepth = 64

// audioQueueDepth bounds per-connection TTS chunk buffering. Full audio
// queues drop the chunk rather than kicking the client — audio lag must not
// tear down the control/event stream.
const audioQueueDepth = 128

// maxStreamClients caps concurrent WebSocket connections — this is a
// single-user tool, not a service under load.
const maxStreamClients = 8

type streamConn struct {
	out   chan []byte // event / control envelopes
	audio chan []byte // TTS audio_chunk / audio_end / audio_reset only
	kick  chan string
	once  sync.Once

	// token is the bearer that opened this stream (primary or D2 device).
	// Used to evict only that device's sockets on DELETE /v1/devices/{id}.
	token string

	// audioStream is a per-connection preference set by the audio_output
	// control message. atomic so the hub hot path never takes a second lock.
	audioStream atomic.Bool
	// audioBlocked is set for every stream client when the shared turn is
	// interrupted. It stays set until the next turn starts, preventing tail
	// chunks from the canceled turn from crossing the reset boundary.
	audioBlocked atomic.Bool
	// voiceQueued marks a voice turn this connection has queued behind
	// another turn and that has not claimed the mic yet. Cleared when the
	// turn is dequeued, when the client gives up (voice_end), or when the
	// connection goes away — a cleared flag abandons the queued turn.
	voiceQueued atomic.Bool
}

func (c *streamConn) evict(reason string) {
	c.once.Do(func() { c.kick <- reason })
}

func (c *streamConn) wantsAudio() bool {
	return c.audioStream.Load()
}

// hub owns the single bus subscription and fans events out to connections.
// The bus has one detachable SubscribeAll handler for the server's lifetime;
// connections attach and detach here, never on the bus itself.
type hub struct {
	mu           sync.Mutex
	conns        map[*streamConn]struct{}
	audioBlocked bool

	// streamers counts connections with audio_output mode "stream" so the
	// TTS pump can skip encode/marshal when nobody is listening.
	streamers atomic.Int32

	// micClaimant is the exclusive remote-mic owner (push-to-talk).
	micClaimant *streamConn
	// ingress receives remote PCM when remote mic is enabled.
	ingress *audio.Ingress

	// speaking reports whether the shared turn is in its TTS phase, so a
	// remote interrupt can say what it interrupted. Set on the first
	// speaking_started and cleared at the turn boundary rather than on each
	// segment's speaking_complete: a multi-sentence reply is still speaking
	// between its segments. Written only from the bus subscription.
	speaking atomic.Bool
}

func newHub() *hub {
	return &hub{conns: make(map[*streamConn]struct{})}
}

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

func (h *hub) add(c *streamConn) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.conns) >= maxStreamClients {
		return false
	}
	c.audioBlocked.Store(h.audioBlocked)
	h.conns[c] = struct{}{}
	return true
}

func (h *hub) remove(c *streamConn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.conns[c]; !ok {
		return
	}
	delete(h.conns, c)
	if c.audioStream.Swap(false) {
		h.streamers.Add(-1)
	}
}

// evictAll terminates every live stream at a security boundary such as token
// revocation. Handlers remove themselves from the hub as their sockets close.
func (h *hub) evictAll(reason string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for c := range h.conns {
		c.evict(reason)
	}
	if h.micClaimant != nil && h.ingress != nil {
		h.ingress.Finalize()
		h.micClaimant = nil
	}
}

// evictToken terminates streams authenticated with the given bearer only
// (per-device revoke). Other devices stay connected.
func (h *hub) evictToken(token, reason string) {
	if token == "" {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for c := range h.conns {
		if c.token == token {
			c.evict(reason)
			if h.micClaimant == c && h.ingress != nil {
				h.ingress.Finalize()
				h.micClaimant = nil
			}
		}
	}
}

// setConnAudio updates a connection's stream preference and the hub streamer
// count. Safe to call from the control reader while the hub is live.
func (h *hub) setConnAudio(c *streamConn, on bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.conns[c]; !ok {
		return
	}
	was := c.audioStream.Load()
	if was == on {
		return
	}
	c.audioStream.Store(on)
	if on {
		h.streamers.Add(1)
	} else {
		h.streamers.Add(-1)
	}
}

// hasStreamClients reports whether any connected client wants TTS audio.
// Lock-free so the AudioFanout pump can check before encoding.
func (h *hub) hasStreamClients() bool {
	return h.streamers.Load() > 0
}

// broadcast never blocks: a full event queue evicts that client and reclaims
// the hub slot immediately so maxStreamClients capacity is available for new
// connections without waiting for handleStream to return.
func (h *hub) broadcast(msg []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for c := range h.conns {
		select {
		case c.out <- msg:
		default:
			c.evict("client too slow")
			// If this client was streaming, drop the streamer count now —
			// remove() will also run later, so clear the flag first.
			if c.audioStream.Swap(false) {
				h.streamers.Add(-1)
			}
			delete(h.conns, c)
		}
	}
}

// broadcastAudio delivers TTS wire chunks only to clients that opted into
// stream mode. A full audio queue drops the chunk; it never kicks the client.
func (h *hub) broadcastAudio(msg []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for c := range h.conns {
		if !c.wantsAudio() || c.audioBlocked.Load() {
			continue
		}
		select {
		case c.audio <- msg:
		default:
			// Drop this chunk; keep the connection for events/control.
		}
	}
}

var audioResetEnvelope = []byte(`{"type":"audio_reset"}`)

// resetAudio establishes an ordered cancellation boundary for every stream
// client. Holding the hub lock excludes broadcastAudio while queued tail
// chunks are drained and audio_reset is enqueued. The browser suppresses audio
// as soon as it sends interrupt, then resumes when it receives this marker.
func (h *hub) resetAudio() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.audioBlocked = true
	for c := range h.conns {
		if !c.wantsAudio() {
			continue
		}
		c.audioBlocked.Store(true)
		for {
			select {
			case <-c.audio:
				continue
			default:
			}
			break
		}
		// The queue was just drained while broadcasts were excluded, so this
		// send cannot block.
		c.audio <- audioResetEnvelope
	}
}

// resumeAudio opens the next-turn side of the reset boundary. ThinkingStarted
// is emitted before synthesis, so no new turn audio can race ahead of this.
func (h *hub) resumeAudio() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.audioBlocked = false
	for c := range h.conns {
		c.audioBlocked.Store(false)
	}
}

func (h *hub) attachBus(bus *events.Bus) (detach func()) {
	return bus.SubscribeAll(func(e events.Event) {
		switch e.(type) {
		case events.ThinkingStarted:
			h.speaking.Store(false)
			h.resumeAudio()
		case events.SpeakingStarted:
			h.speaking.Store(true)
		case events.SpeakingInterrupted, events.TurnMetrics:
			h.speaking.Store(false)
		}
		msg, err := marshalEvent(e)
		if err != nil {
			return
		}
		h.broadcast(msg)
	})
}

// handleStream upgrades /v1/stream and bridges the hub to one client.
func (s *Server) handleStream(w http.ResponseWriter, r *http.Request) {
	ws, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}

	conn := &streamConn{
		out:   make(chan []byte, connQueueDepth),
		audio: make(chan []byte, audioQueueDepth),
		kick:  make(chan string, 1),
		token: presentedToken(r),
	}
	if !s.hub.add(conn) {
		ws.Close(websocket.StatusTryAgainLater, "too many clients")
		return
	}
	defer func() {
		// Abandon anything this connection had queued before releasing: a
		// turn dequeued after the socket is gone must not claim the mic.
		conn.voiceQueued.Store(false)
		s.hub.releaseMic(conn)
		s.hub.remove(conn)
	}()

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	writeDone := make(chan struct{})
	go func() {
		defer close(writeDone)
		for {
			select {
			case <-ctx.Done():
				return
			case reason := <-conn.kick:
				ws.Close(websocket.StatusPolicyViolation, reason)
				return
			case msg := <-conn.out:
				if err := ws.Write(ctx, websocket.MessageText, msg); err != nil {
					return
				}
			case msg := <-conn.audio:
				if err := ws.Write(ctx, websocket.MessageText, msg); err != nil {
					return
				}
			}
		}
	}()

	s.readControls(ctx, ws, conn)
	cancel()
	<-writeDone
	ws.Close(websocket.StatusNormalClosure, "")
}

// readControls decodes inbound control messages until the connection dies.
func (s *Server) readControls(ctx context.Context, ws *websocket.Conn, conn *streamConn) {
	for {
		_, data, err := ws.Read(ctx)
		if err != nil {
			return
		}
		if !s.opts.Credentials.tokenActive() {
			conn.evict("credentials revoked")
			return
		}

		var msg controlMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			s.sendError(conn, "malformed control message")
			continue
		}

		switch msg.Type {
		case "text_input":
			if msg.Text == "" {
				s.sendError(conn, "text_input requires text")
				continue
			}
			if err := s.dispatcher.SubmitText(msg.Text); err != nil {
				s.sendError(conn, dispatchErrText(err))
			}
		case "interrupt":
			s.interrupt()
		case "clear_history":
			if err := s.dispatcher.ClearHistory(); err != nil {
				s.sendError(conn, dispatchErrText(err))
			}
		case "audio_output":
			// Phase 3: per-connection preference. mode "stream" opts into
			// TTS audio_chunk delivery; anything else turns it off. Default
			// is off so event-only clients are unaffected.
			mode := "off"
			if msg.Mode == "stream" {
				mode = "stream"
			}
			s.hub.setConnAudio(conn, mode == "stream")
			s.sendAudioOutputAck(conn, mode)
		case "voice_start":
			if err := s.startVoiceTurn(conn); err != nil {
				s.sendError(conn, err.Error())
			}
		case "audio_input":
			samples, err := decodePCMS16LE(msg.Data, msg.SampleRate)
			if err != nil {
				s.sendError(conn, "audio_input: "+err.Error())
				continue
			}
			if err := s.hub.writeMic(conn, samples); err != nil {
				s.sendError(conn, err.Error())
			}
		case "voice_end":
			if err := s.endVoiceTurn(conn); err != nil {
				s.sendError(conn, err.Error())
			}
		case "set_persona":
			s.handleSetPersona(ctx, conn, msg.Name)
		default:
			s.sendError(conn, "unknown control message type: "+msg.Type)
		}
	}
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

// sendAudioOutputAck confirms the applied stream preference on the event
// channel so clients do not have to assume success.
func (s *Server) sendAudioOutputAck(conn *streamConn, mode string) {
	msg, err := json.Marshal(map[string]any{
		"type": "audio_output_ack",
		"mode": mode,
	})
	if err != nil {
		return
	}
	select {
	case conn.out <- msg:
	default:
		conn.evict("client too slow")
	}
}

// handleSetPersona switches the persona used by subsequent turns.
//
// It deliberately does not touch the turn in flight. The dispatcher applies
// the runtime swap after that turn, starts a fresh persona-bound session, and
// only then returns the ack, so applies_to=next_turn is an enforceable contract.
func (s *Server) handleSetPersona(ctx context.Context, conn *streamConn, name string) {
	if s.opts.SetPersona == nil {
		s.sendError(conn, "set_persona is not enabled on this server")
		return
	}
	name = strings.TrimSpace(name)
	if name == "" {
		s.sendError(conn, "set_persona requires a name")
		return
	}
	ack, err := s.dispatcher.SetPersona(ctx, name, s.opts.SetPersona)
	if err != nil {
		s.sendError(conn, err.Error())
		return
	}
	msg, err := json.Marshal(map[string]any{
		"type":         "set_persona_ack",
		"id":           ack.ID,
		"display_name": ack.DisplayName,
		"prompt_hash":  ack.PromptHash,
		"applies_to":   "next_turn",
	})
	if err != nil {
		return
	}
	select {
	case conn.out <- msg:
	default:
		conn.evict("client too slow")
	}
}

// sendError delivers a per-connection error envelope (not broadcast — only
// the offending client sees it).
func (s *Server) sendError(conn *streamConn, text string) {
	msg, err := marshalEvent(events.Error{Stage: "netapi", Message: text})
	if err != nil {
		return
	}
	select {
	case conn.out <- msg:
	default:
		conn.evict("client too slow")
	}
}

func dispatchErrText(err error) string {
	if errors.Is(err, ErrBusy) {
		return "server is busy — try again shortly"
	}
	return err.Error()
}
