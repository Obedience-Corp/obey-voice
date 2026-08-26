// Package speechserve bridges obey-speech/v1 TCP clients to one local
// qwen3-tts-worker child process.
//
// The two sides keep different timelines. The child emits its stdio ready
// unprompted — but only after loading the model, seconds after spawn — while a
// TCP client must say hello first and may never arrive at all. So the bridge
// spawns one child at daemon start, holds its ready line, and replays it (with
// the protocol rewritten) to each client that authenticates.
package speechserve

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/qwen"
)

// ChildSpec is the worker this daemon owns. The caller resolves it from
// config so this package never reads settings itself.
type ChildSpec struct {
	Binary   string
	ModelDir string
	Tier     string
	// Args overrides the worker's arguments (tests script a fake child).
	Args []string
	// Env is the base environment; empty uses the process environment.
	Env []string
}

// childReady is the worker's ready line. Type is parsed for the startup check;
// raw keeps every key the worker actually sent, including ones this build does
// not model, so the client's ready can carry them forward.
type childReady struct {
	Type string
	raw  map[string]json.RawMessage
}

// child is the worker process plus its framed stdio.
type child struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Reader
	wait   chan error

	writeMu sync.Mutex

	ready childReady

	closeOnce sync.Once
}

// startChild spawns the worker and reads the ready line it emits unprompted.
// The wait is the model load, not a network timeout: callers pass a budget
// measured in seconds (cold ready is ~6 s on the lab worker).
func startChild(ctx context.Context, spec ChildSpec, readyTimeout time.Duration) (*child, error) {
	binary, err := filepath.Abs(strings.TrimSpace(spec.Binary))
	if err != nil {
		return nil, fmt.Errorf("resolve speech worker: %w", err)
	}
	args := spec.Args
	if args == nil {
		modelDir, err := filepath.Abs(strings.TrimSpace(spec.ModelDir))
		if err != nil {
			return nil, fmt.Errorf("resolve speech model dir: %w", err)
		}
		args = []string{modelDir}
	}
	// Lifetime is the daemon's, not this call's: the child outlives every
	// client connection. CommandContext is required because
	// ConfigureWorkerCommand installs Cancel.
	cmd := exec.CommandContext(context.Background(), binary, args...)
	qwen.ConfigureWorkerCommand(cmd)
	env := spec.Env
	if env == nil {
		env = os.Environ()
	}
	cmd.Env = qwen.NativeWorkerEnv(env, filepath.Dir(binary), spec.Tier)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("speech worker stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, fmt.Errorf("speech worker stdout: %w", err)
	}
	if cmd.Stderr == nil {
		cmd.Stderr = io.Discard
	}
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		return nil, fmt.Errorf("start speech worker: %w", err)
	}

	c := &child{cmd: cmd, stdin: stdin, stdout: bufio.NewReader(stdout), wait: make(chan error, 1)}
	go func() { c.wait <- cmd.Wait() }()

	if err := c.readReady(ctx, readyTimeout); err != nil {
		c.Close()
		return nil, err
	}
	return c, nil
}

func (c *child) readReady(ctx context.Context, timeout time.Duration) error {
	lines := make(chan string, 1)
	errs := make(chan error, 1)
	go func() {
		line, err := c.stdout.ReadString('\n')
		if err != nil {
			errs <- err
			return
		}
		lines <- line
	}()

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case line := <-lines:
		raw := map[string]json.RawMessage{}
		if err := json.Unmarshal([]byte(line), &raw); err != nil {
			return fmt.Errorf("speech worker ready json: %w (%q)", err, strings.TrimSpace(line))
		}
		var kind string
		if value, ok := raw["type"]; ok {
			_ = json.Unmarshal(value, &kind)
		}
		if kind != "ready" {
			return fmt.Errorf("speech worker handshake failed: type=%s", kind)
		}
		c.ready = childReady{Type: kind, raw: raw}
		return nil
	case err := <-errs:
		return fmt.Errorf("speech worker ready: %w", err)
	case err := <-c.wait:
		return fmt.Errorf("speech worker exited during startup: %v", err)
	case <-timer.C:
		return fmt.Errorf("speech worker did not become ready within %s", timeout)
	case <-ctx.Done():
		return ctx.Err()
	}
}

// WriteLine sends one control line to the worker.
func (c *child) WriteLine(v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	_, err = c.stdin.Write(append(data, '\n'))
	return err
}

// Close shuts the worker down. Only daemon exit reaches this: a client's
// shutdown is intercepted upstream so the next conversation still finds a warm
// model (spec/30 §3, D20).
func (c *child) Close() {
	c.closeOnce.Do(func() {
		if c.stdin != nil {
			c.writeMu.Lock()
			_, _ = c.stdin.Write([]byte(`{"type":"shutdown"}` + "\n"))
			c.writeMu.Unlock()
			_ = c.stdin.Close()
		}
		if c.cmd == nil || c.cmd.Process == nil {
			return
		}
		select {
		case <-c.wait:
		case <-time.After(3 * time.Second):
			_ = c.cmd.Process.Kill()
			<-c.wait
		}
	})
}
