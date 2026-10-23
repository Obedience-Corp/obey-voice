package brain

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"time"
)

// codexRPC owns one stdio app-server. Calls are serialized by its owner; the
// reader and writer never block cancellation on a pipe or a full event queue.
type codexRPC struct {
	cmd      *exec.Cmd
	stdin    io.WriteCloser
	messages chan codexMessage
	readErr  chan error
	writes   chan codexWrite
	stop     chan struct{}
	exited   chan struct{}
	nextID   int
	readDone chan struct{}
}
type codexMessage struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *codexRPCError  `json:"error,omitempty"`
}
type codexRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *codexRPCError) Error() string { return fmt.Sprintf("Codex RPC %d: %s", e.Code, e.Message) }

type codexWrite struct {
	data any
	done chan error
}

func startCodexRPC(ctx context.Context, binary, cwd string) (*codexRPC, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// Suppress user hooks even during thread startup. Turn policy remains local
	// to this child process and never writes ~/.codex/config.toml.
	cmd := exec.CommandContext(context.Background(), binary, "app-server", "--listen", "stdio://",
		"-c", "features.codex_hooks=false", "-c", "notify=[]")
	configureCommandCancellation(cmd)
	cmd.Dir = cwd
	cmd.WaitDelay = time.Second
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, err
	}
	// Protocol errors carry actionable details. Never copy arbitrary stderr
	// (which may contain prompts or credentials) into Obey's event stream.
	cmd.Stderr = io.Discard
	if err = cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		return nil, fmt.Errorf("start Codex app-server: %w", err)
	}
	c := &codexRPC{cmd: cmd, stdin: stdin, messages: make(chan codexMessage, 64), readErr: make(chan error, 1), writes: make(chan codexWrite), stop: make(chan struct{}), exited: make(chan struct{}), readDone: make(chan struct{})}
	go c.read(stdout)
	go c.writeLoop()
	go func() { <-c.readDone; _ = cmd.Wait(); close(c.exited) }()
	return c, nil
}
func (c *codexRPC) read(r io.Reader) {
	defer close(c.readDone)
	defer close(c.messages)
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 8*1024*1024)
	for scanner.Scan() {
		var m codexMessage
		if err := json.Unmarshal(scanner.Bytes(), &m); err != nil {
			c.readErr <- errors.New("codex app-server sent invalid JSON")
			return
		}
		select {
		case c.messages <- m:
		case <-c.stop:
			return
		}
	}
	err := scanner.Err()
	if err == nil {
		err = io.ErrUnexpectedEOF
	}
	c.readErr <- fmt.Errorf("codex app-server stream ended: %w", err)
}
func (c *codexRPC) writeLoop() {
	encoder := json.NewEncoder(c.stdin)
	for {
		select {
		case w := <-c.writes:
			w.done <- encoder.Encode(w.data)
		case <-c.stop:
			return
		}
	}
}
func (c *codexRPC) send(ctx context.Context, data any) error {
	w := codexWrite{data: data, done: make(chan error, 1)}
	select {
	case c.writes <- w:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case err := <-w.done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (c *codexRPC) next(ctx context.Context) (codexMessage, error) {
	select {
	case m, ok := <-c.messages:
		if ok {
			return m, nil
		}
		select {
		case err := <-c.readErr:
			return m, err
		case <-ctx.Done():
			return m, ctx.Err()
		}
	case <-ctx.Done():
		return codexMessage{}, ctx.Err()
	}
}
func (c *codexRPC) call(ctx context.Context, method string, params, result any, notice func(codexMessage) error) error {
	c.nextID++
	id := c.nextID
	if err := c.send(ctx, map[string]any{"id": id, "method": method, "params": params}); err != nil {
		return fmt.Errorf("%s: %w", method, err)
	}
	for {
		m, err := c.next(ctx)
		if err != nil {
			return fmt.Errorf("%s: %w", method, err)
		}
		if m.Method != "" {
			if len(m.ID) > 0 {
				return c.reject(ctx, m)
			}
			if notice != nil {
				if err := notice(m); err != nil {
					return err
				}
			}
			continue
		}
		if string(m.ID) != fmt.Sprint(id) {
			continue
		}
		if m.Error != nil {
			return fmt.Errorf("%s: %w", method, m.Error)
		}
		if result != nil {
			if err := json.Unmarshal(m.Result, result); err != nil {
				return fmt.Errorf("decode %s: %w", method, err)
			}
		}
		return nil
	}
}
func (c *codexRPC) initialize(ctx context.Context) error {
	if err := c.call(ctx, "initialize", map[string]any{"clientInfo": map[string]string{"name": "obey_voice", "title": "Obey Voice", "version": "1"}}, nil, nil); err != nil {
		return err
	}
	return c.send(ctx, map[string]any{"method": "initialized"})
}
func (c *codexRPC) reject(ctx context.Context, m codexMessage) error {
	reply := map[string]any{"id": m.ID}
	switch m.Method {
	case "item/commandExecution/requestApproval", "item/fileChange/requestApproval":
		reply["result"] = map[string]string{"decision": "cancel"}
	case "item/tool/requestUserInput":
		reply["result"] = map[string]any{"answers": map[string]any{}}
	default:
		reply["error"] = map[string]any{"code": -32601, "message": "Obey Voice does not support interactive requests"}
	}
	if err := c.send(ctx, reply); err != nil {
		return err
	}
	return fmt.Errorf("codex requested %s; interactive approval/input is not supported in Obey Voice; complete this action in Codex", m.Method)
}
func (c *codexRPC) close() {
	close(c.stop)
	_ = c.stdin.Close()
	select {
	case <-c.exited:
		_ = c.cmd.Cancel() // reap descendants even if their parent exited first
		return
	case <-time.After(250 * time.Millisecond):
	}
	_ = c.cmd.Cancel()
	<-c.exited
}
