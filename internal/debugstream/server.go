// Package debugstream exposes one conversation's bounded diagnostics, never audio.
package debugstream

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/Obedience-Corp/obey-voice/internal/transcript"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

const Schema = "obey-voice.debug/v1"
const maxReaders = 4

type Descriptor struct {
	Schema    string    `json:"schema"`
	ID        string    `json:"id"`
	RuntimeID string    `json:"runtime_id"`
	SessionID string    `json:"session_id"`
	AgentName string    `json:"agent_name"`
	Endpoint  string    `json:"endpoint"`
	Token     string    `json:"token,omitempty"`
	StartedAt time.Time `json:"started_at"`
}

type Server struct {
	descriptor Descriptor
	path       string
	server     *http.Server
	writer     *transcript.Writer
	unsub      func()
	ring       history
	readers    chan struct{}
	once       sync.Once
}

// Start is called off the audio/TUI update path. Only endpoint metadata is saved.
func Start(root, agent string, bus *events.Bus, trace events.TraceContext) (*Server, error) {
	if bus == nil || trace.RuntimeID == "" {
		return nil, fmt.Errorf("debug stream requires a running conversation")
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	s := &Server{readers: make(chan struct{}, maxReaders), ring: history{changed: make(chan struct{})}}
	s.descriptor = Descriptor{Schema: Schema, ID: rand.Text(), RuntimeID: trace.RuntimeID, SessionID: trace.SessionID, AgentName: agent, Endpoint: "http://" + listener.Addr().String(), Token: rand.Text(), StartedAt: time.Now().UTC()}
	s.path = filepath.Join(root, s.descriptor.ID+".json")
	if err := os.MkdirAll(root, 0700); err != nil {
		listener.Close()
		return nil, err
	}
	raw, err := json.Marshal(s.descriptor)
	if err != nil {
		listener.Close()
		return nil, err
	}
	// Exclusive private metadata; discovery ignores incomplete JSON while starting.
	f, err := os.OpenFile(s.path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		listener.Close()
		return nil, err
	}
	_, err = f.Write(raw)
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		os.Remove(s.path)
		listener.Close()
		return nil, err
	}
	s.writer = transcript.NewLiveWriter(transcript.CaptureOptions{CaptureID: s.descriptor.ID, Trace: trace}, s.ring.publish)
	s.unsub = s.writer.Observe(bus)
	s.server = &http.Server{Handler: http.HandlerFunc(s.serve), ReadHeaderTimeout: 2 * time.Second, IdleTimeout: 10 * time.Second, MaxHeaderBytes: 8 << 10}
	go func() { _ = s.server.Serve(listener) }()
	return s, nil
}

func (s *Server) Descriptor() Descriptor { return s.descriptor }
func (s *Server) Close() {
	if s == nil {
		return
	}
	s.once.Do(func() {
		_ = os.Remove(s.path)
		s.unsub()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = s.writer.CloseContext(ctx)
		_ = s.server.Close()
	})
}
func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "read only", http.StatusMethodNotAllowed)
		return
	}
	if r.Header.Get("Origin") != "" || subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+s.descriptor.Token)) != 1 {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	switch r.URL.Path {
	case "/health":
		d := s.descriptor
		d.Token = ""
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(d)
	case "/events":
		select {
		case s.readers <- struct{}{}:
			defer func() { <-s.readers }()
		default:
			http.Error(w, "reader limit", http.StatusServiceUnavailable)
			return
		}
		s.stream(w, r)
	default:
		http.NotFound(w, r)
	}
}
