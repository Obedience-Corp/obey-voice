package netapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/coder/websocket"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/work"
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

// hub owns the single bus subscription and fans events out to connections.
// The bus has one detachable SubscribeAll handler for the server's lifetime;
// connections attach and detach here, never on the bus itself.
type hub struct {
	mu           sync.Mutex
	completionMu sync.Mutex
	conns        map[*streamConn]struct{}
	closing      bool
	audioBlocked bool
	// audioGeneration is the ordered PCM boundary. Reset advances it while
	// blocking delivery; resume advances it again before reopening delivery.
	// Producers carry the generation they observed at admission, so delayed
	// chunks and end markers cannot cross a reset/resume boundary.
	audioGeneration atomic.Uint64

	// streamers counts connections with audio_output mode "stream" so the
	// TTS pump can skip encode/marshal when nobody is listening.
	streamers atomic.Int32

	// micClaimant is the exclusive remote-mic owner (push-to-talk), and
	// micGen is the watchdog generation of the turn it was granted for, so a
	// deadline can tell its own turn's claim from its successor's.
	micClaimant *streamConn
	micGen      uint64
	// ingress receives remote PCM when remote mic is enabled.
	ingress *audio.Ingress
	// completion permit ownership is serialized separately from mu. Audio
	// fences may hold a permit lock while taking mu to broadcast PCM; no
	// coordinator operation may therefore run while mu is held.
	completionCoord  *work.Coordinator
	completionPermit work.CapturePermit
	completionEpoch  uint64
	completionQueued func() bool

	// speaking reports whether the shared turn is in its TTS phase, so a
	// remote interrupt can say what it interrupted. Set on the first
	// speaking_started and cleared at the turn boundary rather than on each
	// segment's speaking_complete: a multi-sentence reply is still speaking
	// between its segments. Written only from the bus subscription.
	speaking atomic.Bool
	// bumpStateRevision advances the dispatcher-owned client state revision.
	// It is set by Server after the hub is created; zero-value hubs used by
	// focused audio tests remain valid.
	bumpStateRevision func()
}

func newHub() *hub {
	h := &hub{conns: make(map[*streamConn]struct{})}
	h.audioGeneration.Store(1)
	return h
}

func (h *hub) setStateRevision(bump func()) {
	h.mu.Lock()
	h.bumpStateRevision = bump
	h.mu.Unlock()
}

func (h *hub) micStateLocked() string {
	if h.micClaimant != nil {
		return "claimed"
	}
	return "idle"
}

func (h *hub) audioGenerationSnapshot() uint64 {
	if g := h.audioGeneration.Load(); g != 0 {
		return g
	}
	// Keep zero-value hubs used by narrow tests safe without making normal hub
	// construction depend on a second initialization path.
	if h.audioGeneration.CompareAndSwap(0, 1) {
		return 1
	}
	return h.audioGeneration.Load()
}

func (h *hub) add(c *streamConn) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closing || len(h.conns) >= maxStreamClients {
		return false
	}
	c.audioBlocked.Store(h.audioBlocked)
	h.conns[c] = struct{}{}
	return true
}

func (h *hub) remove(c *streamConn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.removeLocked(c)
}

// removeLocked detaches c and releases the per-hub accounting that belongs to
// it. Callers hold h.mu. Keeping this in one path matters because a slow
// connection can be evicted by a broadcast or snapshot publish before its
// handler reaches the deferred remove call.
func (h *hub) removeLocked(c *streamConn) {
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
	h.closing = true
	for c := range h.conns {
		c.evict(reason)
	}
	if h.micClaimant != nil {
		h.releaseLocked(h.micClaimant)
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
			h.releaseLocked(c)
		}
	}
}

// setConnAudio updates a connection's stream preference and binary TTS flag
// under the hub lock, so broadcastPCM cannot observe stream-on with binary
// still false. Safe to call from the control reader while the hub is live.
func (h *hub) setConnAudio(c *streamConn, on, binary bool) {
	h.setConnAudioProtocol(c, on, binary, "")
}

// setConnAudioProtocol is the protocol-aware form used by the WebSocket
// control reader. The variadic wrapper above preserves the existing internal
// test and helper call shape.
func (h *hub) setConnAudioProtocol(c *streamConn, on, binary bool, protocol string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.conns[c]; !ok {
		return
	}
	was := c.audioStream.Load()
	if was != on {
		c.audioStream.Store(on)
		if on {
			h.streamers.Add(1)
		} else {
			h.streamers.Add(-1)
		}
	}
	if on {
		// A client may opt in while the shared boundary is blocked. Carry the
		// hub fence onto it before exposing the stream preference so a delayed
		// producer cannot use that control transition to bypass reset.
		c.audioBlocked.Store(h.audioBlocked)
		c.audioBinary.Store(binary)
		if binary && strings.EqualFold(strings.TrimSpace(protocol), "ova2") {
			c.audioProtocol.Store(audioProtocolOVA2)
		} else {
			c.audioProtocol.Store(audioProtocolOVA1)
		}
	} else {
		c.audioBinary.Store(false)
		c.audioProtocol.Store(0)
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
			h.removeLocked(c)
		}
	}
}

// broadcastAudio delivers TTS wire chunks only to clients that opted into
// stream mode. A full audio queue drops the chunk; it never kicks the client.
// The return value is how many clients accepted the send (TTFA only).
func (h *hub) broadcastAudio(msg []byte, generation ...uint64) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(generation) > 0 && generation[0] != 0 && generation[0] != h.audioGenerationSnapshot() {
		return 0
	}
	accepted := 0
	for c := range h.conns {
		if !c.wantsAudio() || c.audioBlocked.Load() {
			continue
		}
		select {
		case c.audio <- msg:
			accepted++
		default:
			// Drop this chunk; keep the connection for events/control.
		}
	}
	return accepted
}

// broadcastPCM delivers one TTS chunk, JSON+base64 to clients that did not
// opt into binary, and legacy OVA1 binary to those that did. AudioFanout uses
// broadcastPCMAt when it has the negotiated OVA2 identity frame available.
func (h *hub) broadcastPCM(jsonMsg, binMsg []byte) int {
	return h.broadcastPCMAt(jsonMsg, binMsg, nil, 0)
}

// broadcastPCMAt is the fenced form used by AudioFanout. OVA1 remains the
// compatibility frame; OVA2 is selected only by clients that negotiated it.
func (h *hub) broadcastPCMAt(jsonMsg, binMsg, binV2Msg []byte, generation uint64) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	if generation != 0 && generation != h.audioGenerationSnapshot() {
		return 0
	}
	accepted := 0
	for c := range h.conns {
		if !c.wantsAudio() || c.audioBlocked.Load() {
			continue
		}
		msg := jsonMsg
		if c.wantsBinaryAudio() {
			msg = binMsg
			if c.audioProtocol.Load() == audioProtocolOVA2 && len(binV2Msg) > 0 {
				msg = binV2Msg
			}
		}
		select {
		case c.audio <- msg:
			accepted++
		default:
		}
	}
	return accepted
}

// Kept byte-for-byte compatible with existing clients. The client advances
// its local generation when this marker arrives; the hub's private generation
// still fences all producers while reset/resume are in flight.
var audioResetEnvelope = []byte(`{"type":"audio_reset"}`)

// resetAudio establishes an ordered cancellation boundary for every stream
// client. Holding the hub lock excludes broadcastAudio while queued tail
// chunks are drained and audio_reset is enqueued. The browser suppresses audio
// as soon as it sends interrupt, then resumes when it receives this marker.
func (h *hub) resetAudio() {
	// Whatever a client was still playing stops here, so the reply is no
	// longer being spoken. Clearing it also makes a repeated interrupt idle:
	// the second one has nothing left to report.
	h.speaking.Store(false)
	h.mu.Lock()
	defer h.mu.Unlock()
	h.audioGeneration.Add(1)
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

// endOfHostPlayback clears the speaking flag at a turn boundary — but only
// when nobody is receiving TTS over the wire.
//
// With audio_output "stream" (and a muted host, which is the default in
// tailscale mode) the fanout finishes the moment the chunks are queued: a
// twenty-second reply reports speaking_complete and turn_metrics about a
// millisecond after it started. The client is still playing it. Trusting the
// host's "playback complete" there would make every barge-in look like an
// idle interrupt and emit no turn_interrupted at all — the one event the
// barge-in exists to produce. For stream clients the flag is cleared by
// dispatcher turn admission or by the interrupt itself (resetAudio), both of which
// really do mean the client stopped hearing the reply.
func (h *hub) endOfHostPlayback() {
	if !h.hasStreamClients() {
		h.speaking.Store(false)
	}
}

// resumeAudio opens the next-turn side of the reset boundary. Dispatcher
// beginTurn calls it before the runner can admit synthesis.
func (h *hub) resumeAudio() uint64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.audioBlocked {
		h.audioGeneration.Add(1)
	}
	h.audioBlocked = false
	for c := range h.conns {
		c.audioBlocked.Store(false)
	}
	return h.audioGenerationSnapshot()
}

func (h *hub) attachBus(bus *events.Bus) (detach func()) {
	return bus.SubscribeAll(func(e events.Event) {
		switch e := e.(type) {
		case events.ThinkingStarted:
			h.speaking.Store(false)
		case events.SpeakingStarted:
			h.speaking.Store(true)
		case events.SpeakingInterrupted:
			h.speaking.Store(false)
		case events.TurnMetrics:
			h.endOfHostPlayback()
		case events.Error:
			// A turn that dies before turn_metrics would otherwise leave the
			// flag set, and the next idle interrupt would claim it cut off
			// speech that had already stopped.
			if e.Stage == "turn" {
				h.endOfHostPlayback()
			}
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
	// coder/websocket defaults to 32 KiB; raise it so a max-size PCM frame
	// (binary or JSON+base64) is rejected by decodePCMS16LEBytes instead of
	// closing the socket with StatusMessageTooBig.
	ws.SetReadLimit(int64(MaxStreamReadBytes))

	conn := &streamConn{
		out:   make(chan []byte, connQueueDepth),
		audio: make(chan []byte, audioQueueDepth),
		kick:  make(chan string, 1),
		token: presentedToken(r),
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	defer func() {
		// The deadline goes with the turn: a socket that really closed has
		// already ended it, and a fire after this would report a turn that
		// ended cleanly as interrupted.
		conn.watch.disarm()
		// Order matters. remove() first, so claimMic can no longer grant this
		// connection anything; releaseMic() second, so a claim granted just
		// before the removal is still cleared. The other order leaves a window
		// where a queued turn claims the mic after the release has run and
		// nothing is left alive to give it back.
		conn.dropStaged()
		s.hub.remove(conn)
		s.hub.releaseMic(conn)
	}()
	registered := false
	if s.opts.CurrentSessionSnapshot != nil && s.dispatcher != nil {
		err := s.dispatcher.Snapshot(ctx, s.opts.CurrentSessionSnapshot, func(installCtx context.Context, snapshot SessionSnapshot) error {
			return s.installSnapshot(installCtx, conn, snapshot)
		})
		if err != nil {
			status, reason := snapshotConnectClose(err)
			ws.Close(status, reason)
			return
		}
		registered = true
	} else {
		registered = s.hub.add(conn)
	}
	if !registered {
		ws.Close(websocket.StatusTryAgainLater, "too many clients")
		return
	}

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
				typ := websocket.MessageText
				if isBinaryAudioFrame(msg) {
					typ = websocket.MessageBinary
				}
				if err := ws.Write(ctx, typ, msg); err != nil {
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
		typ, data, err := ws.Read(ctx)
		if err != nil {
			return
		}
		if !s.opts.Credentials.tokenActive() {
			conn.evict("credentials revoked")
			return
		}

		if typ == websocket.MessageBinary {
			samples, err := decodePCMS16LEBytes(data, audio.SampleRate)
			if err != nil {
				s.sendError(conn, "audio_input: "+err.Error())
				continue
			}
			if err := s.writeVoiceAudio(conn, samples); err != nil {
				s.sendError(conn, err.Error())
			}
			continue
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
			var audioOptions struct {
				Protocol string `json:"protocol"`
			}
			_ = json.Unmarshal(data, &audioOptions)
			protocol := audioOptions.Protocol
			s.hub.setConnAudioProtocol(conn, mode == "stream", msg.Binary, protocol)
			s.sendAudioOutputAck(conn, mode, conn.audioBinary.Load(), protocol)
		case "transcribe_start":
			if err := s.startTranscription(conn, msg.ID); err != nil {
				s.sendError(conn, dispatchErrText(err))
			}
		case "voice_start":
			if err := s.startVoiceTurn(conn); err != nil {
				s.sendError(conn, dispatchErrText(err))
			}
		case "audio_input":
			samples, err := decodePCMS16LE(msg.Data, msg.SampleRate)
			if err != nil {
				s.sendError(conn, "audio_input: "+err.Error())
				continue
			}
			if err := s.writeVoiceAudio(conn, samples); err != nil {
				s.sendError(conn, err.Error())
			}
		case "voice_end":
			if err := s.endVoiceTurn(conn); err != nil {
				s.sendError(conn, err.Error())
			}
		case "set_participation":
			s.handleSetParticipation(ctx, conn, msg)
		case "set_persona":
			s.handleSetPersona(ctx, conn, msg.Name, msg.RequestID)
		default:
			s.sendError(conn, "unknown control message type: "+msg.Type)
		}
	}
}

// sendAudioOutputAck confirms the applied stream preference on the event
// channel so clients do not have to assume success.
func (s *Server) sendAudioOutputAck(conn *streamConn, mode string, binary bool, requestedProtocol ...string) {
	ack := map[string]any{
		"type": "audio_output_ack",
		"mode": mode,
	}
	if binary {
		ack["binary"] = true
		if conn.audioProtocol.Load() == audioProtocolOVA2 {
			ack["protocol"] = "ova2"
		} else if len(requestedProtocol) > 0 && strings.EqualFold(strings.TrimSpace(requestedProtocol[0]), "ova2") {
			// Unsupported/disabled combinations remain legacy binary. Echoing
			// no OVA2 claim keeps negotiation truthful.
			ack["protocol"] = "ova1"
		}
	}
	msg, err := json.Marshal(ack)
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
// the runtime swap after that turn and only then returns the ack, so
// applies_to=next_turn is an enforceable contract.
func (s *Server) handleSetPersona(ctx context.Context, conn *streamConn, name, requestID string) {
	correlationID := ""
	if requestID != "" {
		correlationID = requestID
	}
	if s.opts.SetPersona == nil && s.opts.SetPersonaContext == nil {
		s.sendControlError(conn, correlationID, "set_persona is not enabled on this server")
		return
	}
	name = strings.TrimSpace(name)
	if name == "" {
		s.sendControlError(conn, correlationID, "set_persona requires a name")
		return
	}
	var ack PersonaAck
	var err error
	if s.opts.SetPersonaContext != nil {
		ack, err = s.dispatcher.SetPersonaContext(ctx, name, s.opts.SetPersonaContext)
	} else {
		ack, err = s.dispatcher.SetPersona(ctx, name, s.opts.SetPersona)
	}
	if err != nil {
		s.sendControlError(conn, correlationID, err.Error())
		return
	}
	ackEnvelope := map[string]any{
		"type":         "set_persona_ack",
		"id":           ack.ID,
		"display_name": ack.DisplayName,
		"prompt_hash":  ack.PromptHash,
		"applies_to":   "next_turn",
	}
	if correlationID != "" {
		ackEnvelope["request_id"] = correlationID
	}
	msg, err := json.Marshal(ackEnvelope)
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
	s.sendEvent(conn, events.Error{Stage: "netapi", Message: text})
}

// sendControlError is the correlated form of a control failure. Generic bus
// errors deliberately remain uncorrelated: a client must never settle a
// pending selection from an unrelated turn or transport diagnostic.
func (s *Server) sendControlError(conn *streamConn, requestID, text string) {
	if requestID == "" {
		s.sendError(conn, text)
		return
	}
	msg, err := json.Marshal(map[string]any{
		"type":       "error",
		"stage":      "netapi",
		"message":    text,
		"request_id": requestID,
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

// sendEvent delivers one event to a single connection, for the things only
// that client is owed. Like broadcast it never blocks: a client that cannot
// keep up on events is evicted rather than allowed to stall a live turn.
func (s *Server) sendEvent(conn *streamConn, e events.Event) {
	msg, err := marshalEvent(e)
	if err != nil {
		return
	}
	select {
	case conn.out <- msg:
	default:
		conn.evict("client too slow")
	}
}

func snapshotConnectClose(err error) (websocket.StatusCode, string) {
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return websocket.StatusPolicyViolation, "session snapshot unavailable"
	case errors.Is(err, ErrBusy):
		return websocket.StatusTryAgainLater, "server busy — try again shortly"
	case errors.Is(err, errServerStopping):
		return websocket.StatusTryAgainLater, "server stopping"
	case errors.Is(err, errTooManyStreamClients):
		return websocket.StatusTryAgainLater, "too many clients"
	default:
		return websocket.StatusInternalError, "session snapshot unavailable"
	}
}

func dispatchErrText(err error) string {
	if errors.Is(err, ErrBusy) {
		return "server is busy — try again shortly"
	}
	return err.Error()
}
