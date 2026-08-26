package speechserve

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

// DefaultBind is loopback only: sharing a GPU is an explicit act (D11).
const DefaultBind = "127.0.0.1:" + config.SpeechDefaultPort

// defaultReadyTimeout bounds the child's model load, which is seconds — never
// the dial-and-handshake budget a client or the doctor probe uses.
const defaultReadyTimeout = 2 * time.Minute

// handshakeTimeout bounds a client's hello. A connection that never speaks
// must not hold the single client slot.
const handshakeTimeout = 10 * time.Second

// Options configure one speech-serve daemon.
type Options struct {
	// Bind is host:port; empty means DefaultBind.
	Bind string
	// Token is required on every connection, loopback included.
	Token string
	// TLSConfig is required for any non-loopback bind.
	TLSConfig *tls.Config
	// Child is the worker this daemon owns.
	Child ChildSpec
	// Tier is this host's qwen_tts_model_tier, offered to clients on the ready
	// line. Optional: the child never reports one, and no client may require it.
	Tier string
	// ReadyTimeout bounds the child's model load.
	ReadyTimeout time.Duration
	// Logf receives operator-facing lines; nil discards them.
	Logf func(format string, args ...any)
}

// Server is the stdio↔TCP bridge. It owns one child worker for its whole
// lifetime and serves one client connection at a time.
type Server struct {
	opts     Options
	listener net.Listener
	child    *child

	mu     sync.Mutex
	active *clientConn

	closeOnce sync.Once
}

// New validates the options. It never binds or spawns: the security rules are
// worth failing on before a worker is loaded.
func New(opts Options) (*Server, error) {
	if strings.TrimSpace(opts.Bind) == "" {
		opts.Bind = DefaultBind
	}
	if strings.TrimSpace(opts.Token) == "" {
		return nil, errors.New("speech-serve requires a token; every connection authenticates, loopback included")
	}
	host, _, err := net.SplitHostPort(opts.Bind)
	if err != nil {
		return nil, fmt.Errorf("--bind %q must be host:port: %w", opts.Bind, err)
	}
	if !bindIsLoopback(host) && opts.TLSConfig == nil {
		return nil, fmt.Errorf("--bind %q leaves this computer without TLS; pass --tls-cert/--tls-key or --tailscale, or bind %s", opts.Bind, DefaultBind)
	}
	if strings.TrimSpace(opts.Child.Binary) == "" {
		return nil, errors.New("speech-serve needs a qwen3-tts worker; install the native package with 'obey-voice models ensure --tts'")
	}
	if opts.ReadyTimeout <= 0 {
		opts.ReadyTimeout = defaultReadyTimeout
	}
	return &Server{opts: opts}, nil
}

// bindIsLoopback reports whether a bind address reaches only this computer.
// An empty host (":7263") or 0.0.0.0 reaches the network and is not loopback.
func bindIsLoopback(host string) bool {
	host = strings.TrimSpace(host)
	if host == "" {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// Start spawns the worker, waits out its model load, and only then binds. A
// port that answers therefore means a worker that can speak (D19): an early
// client is refused at connect instead of hanging on a load it cannot see.
func (s *Server) Start(ctx context.Context) error {
	worker, err := startChild(ctx, s.opts.Child, s.opts.ReadyTimeout)
	if err != nil {
		return err
	}
	s.child = worker

	listener, err := net.Listen("tcp", s.opts.Bind)
	if err != nil {
		worker.Close()
		s.child = nil
		return fmt.Errorf("listen %s: %w", s.opts.Bind, err)
	}
	if s.opts.TLSConfig != nil {
		listener = tls.NewListener(listener, s.opts.TLSConfig)
	}
	s.listener = listener
	go s.pump()
	return nil
}

// Addr is the bound address, valid after Start.
func (s *Server) Addr() string {
	if s.listener == nil {
		return ""
	}
	return s.listener.Addr().String()
}

// Serve accepts connections until ctx is cancelled or the listener closes.
// Exactly one client is served at a time; a second is refused rather than
// queued, so a stale connection can never hide a live one.
func (s *Server) Serve(ctx context.Context) error {
	go func() {
		<-ctx.Done()
		_ = s.listener.Close()
	}()
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			var netErr net.Error
			if errors.As(err, &netErr) && netErr.Timeout() {
				continue
			}
			return err
		}
		go s.handleConn(ctx, conn)
	}
}

// Run is Start, Serve and Close: the daemon's whole life.
func (s *Server) Run(ctx context.Context) error {
	if err := s.Start(ctx); err != nil {
		return err
	}
	defer s.Close()
	s.logf("speech worker ready; listening on %s", s.Addr())
	return s.Serve(ctx)
}

// Close stops accepting and shuts the worker down. This is the only path that
// sends shutdown to the child.
func (s *Server) Close() {
	s.closeOnce.Do(func() {
		if s.listener != nil {
			_ = s.listener.Close()
		}
		if s.child != nil {
			s.child.Close()
		}
	})
}

func (s *Server) logf(format string, args ...any) {
	if s.opts.Logf == nil {
		return
	}
	s.opts.Logf(format, args...)
}
