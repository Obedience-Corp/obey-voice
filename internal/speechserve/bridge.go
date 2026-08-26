package speechserve

import (
	"bufio"
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

// Refusal messages. Auth failures all say the same thing: a client learns
// whether it is welcome, never why not.
const (
	msgUnauthorized  = "unauthorized"
	msgBusy          = "another client is connected"
	msgOneInFlight   = "one synthesize at a time"
	msgWorkerStopped = "speech worker stopped"
)

// drainTimeout bounds how long a departed client's request may hold the single
// client slot. A Stage A worker only acts on cancel between requests, so the
// normal wait is the rest of that synthesis — seconds. The cap exists for a
// wedged worker; id-tagged routing still keeps the next client clean if it
// fires.
const drainTimeout = 2 * time.Minute

// clientConn is the connection currently bridged to the child.
type clientConn struct {
	conn net.Conn

	writeMu sync.Mutex

	activeMu sync.Mutex
	activeID string
	// finished closes when the in-flight request reaches final or error. The
	// slot is held until then even after the socket dies, so the child's
	// remaining frames can never be routed to whoever connects next.
	finished chan struct{}
}

func (c *clientConn) write(p []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	_, err := c.conn.Write(p)
	return err
}

func (c *clientConn) writeJSON(v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return c.write(append(data, '\n'))
}

func (c *clientConn) claim(id string) bool {
	c.activeMu.Lock()
	defer c.activeMu.Unlock()
	if c.activeID != "" {
		return false
	}
	c.activeID = id
	c.finished = make(chan struct{})
	return true
}

func (c *clientConn) release(id string) {
	c.activeMu.Lock()
	defer c.activeMu.Unlock()
	if id != "" && c.activeID != id {
		return
	}
	c.activeID = ""
	if c.finished != nil {
		close(c.finished)
		c.finished = nil
	}
}

func (c *clientConn) inFlight() string {
	c.activeMu.Lock()
	defer c.activeMu.Unlock()
	return c.activeID
}

// owns reports whether a frame belongs to this connection's request. Frames
// carrying no id (a worker-level error) belong to whatever is in flight.
func (c *clientConn) owns(id string) bool {
	c.activeMu.Lock()
	defer c.activeMu.Unlock()
	if id == "" {
		return true
	}
	return c.activeID == id
}

// inFlightState returns the current request and the channel that closes when
// it ends.
func (c *clientConn) inFlightState() (string, chan struct{}) {
	c.activeMu.Lock()
	defer c.activeMu.Unlock()
	return c.activeID, c.finished
}

// handleConn runs one client: hello, ready, then the two directions.
func (s *Server) handleConn(ctx context.Context, conn net.Conn) {
	defer conn.Close()

	client := &clientConn{conn: conn}

	// Authenticate before claiming the client slot: an unauthorized (or silent)
	// connection must not hold the daemon's only slot for the handshake window.
	reader := bufio.NewReader(conn)
	_ = conn.SetReadDeadline(time.Now().Add(handshakeTimeout))
	line, err := reader.ReadString('\n')
	if err != nil {
		return
	}
	if !s.authorize(line) {
		_ = client.writeJSON(errorFrame("", msgUnauthorized))
		s.logf("rejected handshake from %s", conn.RemoteAddr())
		return
	}
	_ = conn.SetReadDeadline(time.Time{})

	if s.workerStopped() {
		_ = client.writeJSON(errorFrame("", msgWorkerStopped))
		return
	}
	if !s.acquire(client) {
		_ = client.writeJSON(errorFrame("", msgBusy))
		s.logf("refused a second client from %s", conn.RemoteAddr())
		return
	}
	// The slot is released only after any in-flight request has ended, so the
	// child's remaining frames cannot reach the next client.
	defer s.releaseClient(client)
	defer s.drainInFlight(client)

	ready, err := s.readyFrame()
	if err != nil {
		_ = client.writeJSON(errorFrame("", msgUnauthorized))
		return
	}
	if err := client.write(ready); err != nil {
		return
	}
	s.logf("client %s connected", conn.RemoteAddr())
	s.clientLoop(ctx, client, reader)
}

// authorize accepts only a well-formed hello carrying this daemon's token.
// The child never sees this line.
func (s *Server) authorize(line string) bool {
	var hello config.SpeechHello
	if err := json.Unmarshal([]byte(line), &hello); err != nil {
		return false
	}
	if hello.Type != "hello" || hello.Protocol != config.SpeechProtocol {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(hello.Token), []byte(s.opts.Token)) == 1
}

// readyFrame is the child's ready line with the protocol rewritten and this
// host's tier added. It starts from the worker's own keys rather than a list of
// the ones this build knows, so a field added to the worker protocol reaches
// clients without a change here (spec/30: extra ready fields are ignored, not
// stripped).
func (s *Server) readyFrame() ([]byte, error) {
	ready := make(map[string]any, len(s.child.ready.raw)+2)
	for key, value := range s.child.ready.raw {
		ready[key] = value
	}
	ready["type"] = "ready"
	ready["protocol"] = config.SpeechProtocol
	if tier := strings.TrimSpace(s.opts.Tier); tier != "" {
		ready["tier"] = tier
	}
	data, err := json.Marshal(ready)
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

// clientLoop forwards client control lines to the child. Two never reach it:
// a second hello, and shutdown — which means "I am done with this connection",
// not "stop the worker" (spec/30 §3, D20).
func (s *Server) clientLoop(ctx context.Context, client *clientConn, reader *bufio.Reader) {
	for {
		if ctx.Err() != nil {
			return
		}
		line, err := reader.ReadString('\n')
		if err != nil {
			return
		}
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		var head struct {
			Type string `json:"type"`
			ID   string `json:"id"`
		}
		if err := json.Unmarshal([]byte(trimmed), &head); err != nil {
			_ = client.writeJSON(errorFrame("", "malformed control line"))
			continue
		}
		switch head.Type {
		case "hello":
			_ = client.writeJSON(errorFrame("", msgUnauthorized))
			return
		case "shutdown":
			s.logf("client %s said shutdown; worker stays warm", client.conn.RemoteAddr())
			return
		case "synthesize":
			if !client.claim(head.ID) {
				_ = client.writeJSON(errorFrame(head.ID, msgOneInFlight))
				continue
			}
			if err := s.child.WriteLine(json.RawMessage(trimmed)); err != nil {
				client.release(head.ID)
				_ = client.writeJSON(errorFrame(head.ID, "speech worker is not accepting requests"))
				return
			}
		case "cancel":
			if err := s.child.WriteLine(json.RawMessage(trimmed)); err != nil {
				return
			}
		default:
			_ = client.writeJSON(errorFrame(head.ID, fmt.Sprintf("unsupported control type %q", head.Type)))
		}
	}
}

// drainInFlight abandons whatever this client asked for and waits for the
// worker to finish answering it. Cancelling alone is not enough: a Stage A
// worker may only notice the cancel after the audio it already generated, and
// those frames must land while this client still owns the slot.
func (s *Server) drainInFlight(client *clientConn) {
	id, finished := client.inFlightState()
	if id == "" {
		return
	}
	_ = s.child.WriteLine(map[string]string{"type": "cancel", "id": id})
	if finished == nil {
		return
	}
	select {
	case <-finished:
	case <-time.After(drainTimeout):
		s.logf("speech worker never finished %s; releasing the client slot", id)
		client.release(id)
	}
}

func (s *Server) acquire(client *clientConn) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active != nil {
		return false
	}
	s.active = client
	return true
}

func (s *Server) releaseClient(client *clientConn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active == client {
		s.active = nil
	}
}

func (s *Server) currentClient() *clientConn {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.active
}

// pump forwards the child's stdout to whichever client is connected. It runs
// for the daemon's life, not the connection's: a client that disappears
// mid-synthesis must not leave the next one reading someone else's PCM, so
// frames with no client are consumed and dropped.
func (s *Server) pump() {
	for {
		line, err := s.child.stdout.ReadString('\n')
		if err != nil {
			s.failClient(err)
			return
		}
		client := s.currentClient()
		var head struct {
			Type     string `json:"type"`
			ID       string `json:"id"`
			NSamples int    `json:"n_samples"`
		}
		_ = json.Unmarshal([]byte(strings.TrimSpace(line)), &head)

		// Route by request id, not by "whoever is connected". A frame from a
		// request the current client never made belongs to nobody: it is
		// consumed and dropped, so a departed client's audio can never be
		// delivered to its successor.
		owner := client
		if owner != nil && !owner.owns(head.ID) {
			owner = nil
		}
		if owner != nil {
			_ = owner.write([]byte(line))
		}
		if head.Type == "pcm_meta" && head.NSamples > 0 {
			if err := s.forwardPCM(owner, head.NSamples); err != nil {
				s.failClient(err)
				return
			}
		}
		if head.Type == "final" || head.Type == "error" {
			if owner != nil {
				owner.release(head.ID)
			} else if client != nil {
				// Still clear a stale claim so the slot can be released.
				client.release(head.ID)
			}
		}
	}
}

// forwardPCM copies the raw f32le payload that follows a pcm_meta, plus the
// Stage A trailing newline when the worker writes one, byte for byte.
func (s *Server) forwardPCM(client *clientConn, samples int) error {
	var sink io.Writer = io.Discard
	if client != nil {
		sink = &clientWriter{client: client}
	}
	if _, err := io.CopyN(sink, s.child.stdout, int64(samples)*4); err != nil {
		return err
	}
	// The trailing newline is optional in the child contract (Stage A writes
	// one; a streaming chunk need not). Peek rather than read: nothing is
	// consumed unless it really is the trailer, so the stream cannot desync on
	// a worker that omits it.
	next, err := s.child.stdout.Peek(1)
	if err != nil {
		return err
	}
	if next[0] != '\n' {
		return nil
	}
	if _, err := s.child.stdout.Discard(1); err != nil {
		return err
	}
	if client != nil {
		_ = client.write([]byte{'\n'})
	}
	return nil
}

// clientWriter adapts a connection's guarded write to io.Writer. A failed
// client write is not fatal to the bridge: the payload still has to be drained
// out of the child's pipe.
type clientWriter struct{ client *clientConn }

func (w *clientWriter) Write(p []byte) (int, error) {
	_ = w.client.write(p)
	return len(p), nil
}

// failClient reports a dead worker to whoever is connected. The daemon does not
// restart the child: the operator started it, and the host falls back on its
// own (E4).
func (s *Server) failClient(cause error) {
	s.markWorkerStopped()
	s.logf("speech worker stopped: %v", cause)
	client := s.currentClient()
	if client == nil {
		return
	}
	id := client.inFlight()
	_ = client.writeJSON(errorFrame(id, msgWorkerStopped))
	// Free the drain: no further frames are coming.
	client.release(id)
	_ = client.conn.Close()
}

func errorFrame(id, message string) map[string]string {
	frame := map[string]string{"type": "error", "message": message}
	if id != "" {
		frame["id"] = id
	}
	return frame
}
