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
	// Whatever a client was still playing stops here, so the reply is no
	// longer being spoken. Clearing it also makes a repeated interrupt idle:
	// the second one has nothing left to report.
	h.speaking.Store(false)
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

// endOfHostPlayback clears the speaking flag at a turn boundary — but only
// when nobody is receiving TTS over the wire.
//
// With audio_output "stream" (and a muted host, which is the default in
// tailscale mode) the fanout finishes the moment the chunks are queued: a
// twenty-second reply reports speaking_complete and turn_metrics about a
// millisecond after it started. The client is still playing it. Trusting the
// host's "playback complete" there would make every barge-in look like an
// idle interrupt and emit no turn_interrupted at all — the one event the
// barge-in exists to produce. For stream clients the flag is cleared by the
// next thinking_started or by the interrupt itself (resetAudio), both of which
// really do mean the client stopped hearing the reply.
func (h *hub) endOfHostPlayback() {
	if !h.hasStreamClients() {
		h.speaking.Store(false)
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
		switch e := e.(type) {
		case events.ThinkingStarted:
			h.speaking.Store(false)
			h.resumeAudio()
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
		// Order matters. remove() first, so claimMic can no longer grant this
		// connection anything; releaseMic() second, so a claim granted just
		// before the removal is still cleared. The other order leaves a window
		// where a queued turn claims the mic after the release has run and
		// nothing is left alive to give it back.
		conn.voiceQueued.Store(false)
		conn.dropStaged()
		s.hub.remove(conn)
		s.hub.releaseMic(conn)
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
		case "set_persona":
			s.handleSetPersona(ctx, conn, msg.Name)
		default:
			s.sendError(conn, "unknown control message type: "+msg.Type)
		}
	}
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
