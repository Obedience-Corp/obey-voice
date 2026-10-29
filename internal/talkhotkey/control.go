package talkhotkey

import (
	"bufio"
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/discussion"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// The control endpoint is the talk key for every OS that will not let a
// background process grab a key for itself. The running conversation listens
// on a private per-user endpoint and any shortcut tool — sway, Hyprland, GNOME,
// Karabiner, Raycast — sends it one line. The commands land on the same Driver
// as the native helper's key edges, so the mode semantics are identical and
// there is one behavioral contract, not two.
//
// The protocol is one command per line, answered with "ok" or "error: why":
//
//	down    open a turn
//	up      close it, reporting how long the key was held
//	press   both edges at once — a tap
//	ping    liveness only; it answers and emits nothing
//
// One command per dial is the recommended shape and what the recipes do. A
// client may also keep one connection and send several commands through it;
// the idle bound covers only the wait for the first, so a hold of any length
// still delivers its up on the same connection.

// ErrNoListener is what a shortcut tool gets when the conversation is not
// running. It is the ordinary case (you pressed the key before starting a
// conversation), so it reads as a sentence rather than a dial error.
var ErrNoListener = errors.New("talk control: no obey-voice conversation is listening")

const (
	controlDirName    = "obey-voice"
	controlSocketName = "talk.sock"
	controlInfoName   = "talk-control.json"

	// controlDialTimeout bounds the liveness probe of a socket file, or a
	// record, we found in the way. Loopback answers in microseconds.
	controlDialTimeout = 200 * time.Millisecond

	// controlLineLimit caps one command line. Commands are a few bytes and the
	// token line is 70; anything longer is not a shortcut tool.
	controlLineLimit = 2048
)

// controlIdleTimeout bounds the wait for a connection's first command and
// every reply write. A shortcut tool sends a line and leaves; a connection
// that dials and says nothing is not a client. A held key is, so the read
// bound is dropped once a command has arrived. A var so tests can shrink it.
var controlIdleTimeout = 5 * time.Second

// useControlTCP selects the Windows shape: a loopback port plus a token
// recorded in a file, rather than a unix socket in a 0700 directory. A var so
// the TCP path — including its info-file races — is testable from any host.
var useControlTCP = runtime.GOOS == "windows"

// Control is the conversation's side: a listener, the events its commands
// produced, and the file it has to remove when it lets go.
type Control struct {
	requestCtx    context.Context
	stopRequests  context.CancelFunc
	participation func(context.Context, *discussion.Settings) (discussion.Settings, error)
	listener      net.Listener
	events        chan Event
	addr          string
	token         string
	cleanup       func()

	handlers sync.WaitGroup
	accepted chan struct{}
	stopCtx  func() bool
	once     sync.Once

	mu     sync.Mutex
	conns  map[net.Conn]struct{}
	closed bool
	downAt time.Time
}

// ListenControl opens the endpoint and starts accepting commands. It stops
// when ctx is done or Close is called, whichever comes first.
func ListenControl(ctx context.Context) (*Control, error) {
	if useControlTCP {
		return listenControlTCP(ctx)
	}
	return listenControlUnix(ctx)
}

func listenControlUnix(ctx context.Context) (*Control, error) {
	path := controlSocketPath()
	// 0700 is the access control: the socket lives in a directory only this
	// user can enter, so no other account can send this conversation a turn.
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create the talk control directory: %w", err)
	}
	listener, err := listenUnix(ctx, path)
	if err != nil {
		return nil, err
	}
	return newControl(ctx, listener, path, "", func() { _ = os.Remove(path) }), nil
}

// listenUnix binds path, clearing a socket file left behind by a conversation
// that died. A file that still answers belongs to a live conversation and is
// never removed — two TUIs would otherwise steal the key from each other.
func listenUnix(ctx context.Context, path string) (net.Listener, error) {
	var config net.ListenConfig
	listener, err := config.Listen(ctx, "unix", path)
	if err == nil {
		return listener, nil
	}
	if controlAnswers(path) {
		return nil, fmt.Errorf("talk control: another obey-voice conversation is listening on %s", path)
	}
	if rmErr := os.Remove(path); rmErr != nil && !errors.Is(rmErr, os.ErrNotExist) {
		return nil, fmt.Errorf("remove the stale talk control socket %s: %w", path, rmErr)
	}
	listener, err = config.Listen(ctx, "unix", path)
	if err != nil {
		return nil, fmt.Errorf("listen on the talk control socket %s: %w", path, err)
	}
	return listener, nil
}

func controlAnswers(path string) bool {
	conn, err := net.DialTimeout("unix", path, controlDialTimeout)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// listenControlTCP takes a loopback port and records it. The record is the
// endpoint's name here, so it gets the same liveness rule listenUnix applies to
// a socket file: a record that still answers belongs to a live conversation and
// is left alone. Overwriting one would point every client at the newest TUI and
// let the first one out delete the record of the one still running.
func listenControlTCP(ctx context.Context) (*Control, error) {
	infoPath, err := controlInfoPath()
	if err != nil {
		return nil, err
	}
	if existing, err := readControlInfo(infoPath); err == nil && controlInfoAnswers(existing) {
		return nil, fmt.Errorf("talk control: another obey-voice conversation is already listening at %s", existing.Address)
	}
	var config net.ListenConfig
	listener, err := config.Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("listen on the talk control port: %w", err)
	}
	token, err := controlToken()
	if err != nil {
		_ = listener.Close()
		return nil, err
	}
	info := controlInfoFile{Address: listener.Addr().String(), Token: token}
	if err := writeControlInfo(infoPath, info); err != nil {
		_ = listener.Close()
		return nil, err
	}
	return newControl(ctx, listener, info.Address, token, func() { removeControlInfo(infoPath, info) }), nil
}

// controlInfoAnswers reports whether the endpoint a record names is a live
// conversation. The token handshake is part of the check: loopback ports are
// reused, and something else that happens to hold the port is not us.
func controlInfoAnswers(info controlInfoFile) bool {
	conn, err := net.DialTimeout("tcp", info.Address, controlDialTimeout)
	if err != nil {
		return false
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(controlDialTimeout))
	reader := bufio.NewReader(io.LimitReader(conn, controlLineLimit*4))
	if info.Token != "" {
		if err := exchange(conn, reader, "token "+info.Token); err != nil {
			return false
		}
	}
	return exchange(conn, reader, "ping") == nil
}

func newControl(ctx context.Context, listener net.Listener, address, token string, cleanup func()) *Control {
	requestCtx, stopRequests := context.WithCancel(ctx)
	control := &Control{requestCtx: requestCtx, stopRequests: stopRequests,
		listener: listener,
		events:   make(chan Event, eventBuffer),
		addr:     address,
		token:    token,
		cleanup:  cleanup,
		accepted: make(chan struct{}),
		conns:    make(map[net.Conn]struct{}),
	}
	control.stopCtx = context.AfterFunc(ctx, func() { _ = control.Close() })
	go control.accept()
	return control
}

// Events streams the turn edges commands produced. It closes once Close has
// stopped the endpoint, so a reader learns the endpoint is gone.
func (c *Control) Events() <-chan Event { return c.events }

// Address is the endpoint in the form a human can act on: the socket path, or
// the loopback host:port.
func (c *Control) Address() string { return c.addr }

// Close stops the endpoint and removes what it put on disk. Idempotent, and it
// returns only once nothing can emit any more.
func (c *Control) Close() error {
	var err error
	c.once.Do(func() {
		if c.stopRequests != nil {
			c.stopRequests()
		}
		if c.stopCtx != nil {
			c.stopCtx()
		}
		err = c.listener.Close()
		c.dropConnections()
		<-c.accepted
		c.handlers.Wait()
		close(c.events)
		if c.cleanup != nil {
			c.cleanup()
		}
	})
	return err
}

// dropConnections closes every open connection so an idle client cannot hold
// shutdown for the length of its read deadline.
func (c *Control) dropConnections() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	for conn := range c.conns {
		_ = conn.Close()
	}
	clear(c.conns)
}

func (c *Control) accept() {
	defer close(c.accepted)
	for {
		conn, err := c.listener.Accept()
		if err != nil {
			return
		}
		if !c.track(conn) {
			_ = conn.Close()
			return
		}
		c.handlers.Go(func() {
			defer c.release(conn)
			c.serve(conn)
		})
	}
}

func (c *Control) track(conn net.Conn) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return false
	}
	c.conns[conn] = struct{}{}
	return true
}

func (c *Control) release(conn net.Conn) {
	c.mu.Lock()
	delete(c.conns, conn)
	c.mu.Unlock()
	_ = conn.Close()
}

// serve reads one command per line until the client hangs up. Many lines on
// one connection are allowed: a tool that keeps a pipe open sends down and up
// through the same connection.
func (c *Control) serve(conn net.Conn) {
	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 0, 128), controlLineLimit)
	authorized := c.token == ""
	// The read bound covers the handshake and the first command only. After
	// that the connection belongs to a client that is holding the key, and the
	// up it owes may be minutes away — dropping it there would leave the turn
	// open with nothing left to close it.
	_ = conn.SetReadDeadline(time.Now().Add(controlIdleTimeout))
	for {
		if !scanner.Scan() {
			return
		}
		command := strings.TrimSpace(scanner.Text())
		if command == "" {
			continue
		}
		if !authorized {
			if !c.authorize(command) {
				_ = writeReply(conn, "error: unauthorized")
				return
			}
			authorized = true
			if err := writeReply(conn, "ok"); err != nil {
				return
			}
			continue
		}
		if err := c.dispatch(conn, command); err != nil {
			return
		}
		_ = conn.SetReadDeadline(time.Time{})
	}
}

// dispatch runs one command and answers for it. Every accepted command is
// acknowledged, so a shortcut tool that checks the exit status of
// `obey-voice talk` learns whether the turn actually moved.
func (c *Control) dispatch(conn net.Conn, command string) error {
	if payload, ok := strings.CutPrefix(command, "participation "); ok {
		return c.dispatchParticipation(conn, payload)
	}
	events, ok := c.apply(command)
	if !ok {
		return writeReply(conn, "error: unknown command")
	}
	for _, event := range events {
		c.emit(event)
	}
	return writeReply(conn, "ok")
}

func (c *Control) authorize(line string) bool {
	presented, ok := strings.CutPrefix(line, "token ")
	if !ok {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(presented), []byte(c.token)) == 1
}

// apply turns one command into the key edges it stands for. Unknown commands
// produce nothing at all: a typo in a shortcut binding must not open a turn.
func (c *Control) apply(command string) ([]Event, bool) {
	switch command {
	case "down":
		c.mu.Lock()
		c.downAt = time.Now()
		c.mu.Unlock()
		return []Event{{Proto: Proto, Kind: KindDown}}, true
	case "up":
		return []Event{{Proto: Proto, Kind: KindUp, HeldMS: c.takeHeld()}}, true
	case "press":
		// A tap: the two edges arrive together, so there is no duration to
		// report and automatic mode reads it as the tap it is.
		c.takeHeld()
		return []Event{{Proto: Proto, Kind: KindDown}, {Proto: Proto, Kind: KindUp}}, true
	case "ping":
		// Liveness only. It must not touch the turn: a listener probing a
		// record it found in the way would otherwise open somebody's mic.
		return nil, true
	default:
		return nil, false
	}
}

// takeHeld is how long the key was held, measured from the last down this
// endpoint saw. Zero when there was none: an up with no down is still
// delivered, and the driver is the thing that ignores it.
func (c *Control) takeHeld() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.downAt.IsZero() {
		return 0
	}
	held := time.Since(c.downAt).Milliseconds()
	c.downAt = time.Time{}
	return held
}

func (c *Control) emit(event Event) {
	select {
	case c.events <- event:
	default:
	}
}

// writeReply bounds its own write: the read deadline is dropped once a client
// is holding the key, and a client that never reads must not park a handler.
func writeReply(conn net.Conn, reply string) error {
	_ = conn.SetWriteDeadline(time.Now().Add(controlIdleTimeout))
	_, err := io.WriteString(conn, reply+"\n")
	return err
}

// SendControl delivers one command to the running conversation. This is the
// whole client: a shortcut tool runs `obey-voice talk down`, which is one dial,
// one line, and one reply.
func SendControl(ctx context.Context, command string) error {
	network, address, token, err := controlTarget()
	if err != nil {
		return err
	}
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, network, address)
	if err != nil {
		// The dial error names the endpoint, so the sentence reads once.
		return fmt.Errorf("%w: %w", ErrNoListener, err)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(controlIdleTimeout))

	reader := bufio.NewReader(io.LimitReader(conn, controlLineLimit*4))
	if token != "" {
		if err := exchange(conn, reader, "token "+token); err != nil {
			return err
		}
	}
	return exchange(conn, reader, command)
}

func exchange(conn net.Conn, reader *bufio.Reader, line string) error {
	if _, err := io.WriteString(conn, line+"\n"); err != nil {
		return fmt.Errorf("send the talk control command: %w", err)
	}
	reply, err := reader.ReadString('\n')
	if err != nil {
		return fmt.Errorf("read the talk control reply: %w", err)
	}
	reply = strings.TrimSpace(reply)
	if detail, ok := strings.CutPrefix(reply, "error: "); ok {
		return fmt.Errorf("talk control: %s", detail)
	}
	if reply != "ok" {
		return fmt.Errorf("talk control: unexpected reply %q", reply)
	}
	return nil
}
