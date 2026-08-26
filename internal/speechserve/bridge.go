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
	msgUnauthorized = "unauthorized"
	msgBusy         = "another client is connected"
	msgOneInFlight  = "one synthesize at a time"
)

// clientConn is the connection currently bridged to the child.
type clientConn struct {
	conn net.Conn

	writeMu sync.Mutex

	activeMu sync.Mutex
	activeID string
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
	return true
}

func (c *clientConn) release(id string) {
	c.activeMu.Lock()
	defer c.activeMu.Unlock()
	if id == "" || c.activeID == id {
		c.activeID = ""
	}
}

func (c *clientConn) inFlight() string {
	c.activeMu.Lock()
	defer c.activeMu.Unlock()
	return c.activeID
}

// handleConn runs one client: hello, ready, then the two directions.
func (s *Server) handleConn(ctx context.Context, conn net.Conn) {
	defer conn.Close()

	client := &clientConn{conn: conn}
	if !s.acquire(client) {
		_ = client.writeJSON(errorFrame("", msgBusy))
		s.logf("refused a second client from %s", conn.RemoteAddr())
		return
	}
	defer s.releaseClient(client)

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

// readyFrame is the child's ready with the protocol rewritten and this host's
// tier added when it has one. Everything else is copied, so a field this build
// does not model still reaches the client.
func (s *Server) readyFrame() ([]byte, error) {
	ready := map[string]any{
		"type":        "ready",
		"protocol":    config.SpeechProtocol,
		"sample_rate": s.child.ready.SampleRate,
		"pcm_format":  s.child.ready.PCMFormat,
		"streaming":   s.child.ready.Streaming,
	}
	if note := strings.TrimSpace(s.child.ready.Note); note != "" {
		ready["note"] = note
	}
	if len(s.child.ready.Presets) > 0 {
		ready["presets"] = s.child.ready.Presets
	}
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
			s.cancelInFlight(client)
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
			s.cancelInFlight(client)
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

// cancelInFlight tells the worker to abandon whatever this client asked for.
// The pump keeps reading until that request's final so the next client starts
// on a frame boundary.
func (s *Server) cancelInFlight(client *clientConn) {
	id := client.inFlight()
	if id == "" {
		return
	}
	_ = s.child.WriteLine(map[string]string{"type": "cancel", "id": id})
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

		if client != nil {
			_ = client.write([]byte(line))
		}
		if head.Type == "pcm_meta" && head.NSamples > 0 {
			if err := s.forwardPCM(client, head.NSamples); err != nil {
				s.failClient(err)
				return
			}
		}
		if client != nil && (head.Type == "final" || head.Type == "error") {
			client.release(head.ID)
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
	b, err := s.child.stdout.ReadByte()
	if err != nil {
		return err
	}
	if b != '\n' {
		return s.child.stdout.UnreadByte()
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
	client := s.currentClient()
	if client == nil {
		return
	}
	_ = client.writeJSON(errorFrame(client.inFlight(), "speech worker stopped"))
	_ = client.conn.Close()
	s.logf("speech worker stopped: %v", cause)
}

func errorFrame(id, message string) map[string]string {
	frame := map[string]string{"type": "error", "message": message}
	if id != "" {
		frame["id"] = id
	}
	return frame
}
