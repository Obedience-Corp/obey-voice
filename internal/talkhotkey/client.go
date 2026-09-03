package talkhotkey

import (
	"bufio"
	"cmp"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"sync"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/qwen"
)

// DefaultStopTimeout is how long a helper gets to leave on SIGINT before it is
// killed.
const DefaultStopTimeout = 3 * time.Second

// eventBuffer is deep enough for a burst of key edges the reader has not drained
// yet. It is not a queue anybody should fall behind on: the parent reads events
// in its own loop and a stalled reader means the UI is wedged, not that events
// need to be preserved.
const eventBuffer = 64

// Supported reports whether this build can run the helper at all — that is,
// whether the OS lets a process claim a key for itself. macOS (Carbon, no TCC
// prompt) and Windows (RegisterHotKey) do; on Linux only the compositor may,
// so there the talk key arrives through the control endpoint instead.
func Supported() bool { return runtime.GOOS == "darwin" || runtime.GOOS == "windows" }

// Command builds the helper process. Injected so tests can run a scripted fake
// instead of the real Carbon helper. It must build the Cmd with
// exec.CommandContext: the process-group cancellation below needs the context.
type Command func(ctx context.Context, combo Combo) (*exec.Cmd, error)

// Options configure Start. The zero value is the shipped behavior.
type Options struct {
	Command     Command
	StopTimeout time.Duration
}

// DefaultCommand re-runs this binary's hidden talk-hotkey subcommand. Spawning
// ourselves keeps the Carbon code in one binary and lets the helper die with
// the parent instead of outliving it holding a system-wide key.
func DefaultCommand(ctx context.Context, combo Combo) (*exec.Cmd, error) {
	executable, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("locate Obey Voice executable: %w", err)
	}
	return exec.CommandContext(ctx, executable, "talk-hotkey", "--combo", combo.String()), nil
}

// Client is a running helper process and its event stream.
type Client struct {
	cmd         *exec.Cmd
	stdin       io.WriteCloser
	events      chan Event
	done        chan struct{}
	stopTimeout time.Duration
	stopOnce    sync.Once
}

// Start spawns the helper and begins streaming its events. The helper is
// already registered — or already failed — by the time the first event arrives,
// so callers wait on Events rather than on Start.
func Start(ctx context.Context, combo Combo, opts Options) (*Client, error) {
	build := opts.Command
	if build == nil {
		build = DefaultCommand
	}
	cmd, err := build(ctx, combo)
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("capture talk hotkey helper output: %w", err)
	}
	// The helper exits when this pipe closes, which is how it notices a parent
	// that died without stopping it. Held open for the client's lifetime.
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("open talk hotkey helper input: %w", err)
	}
	// Diagnostics are not part of the protocol and must never block the child.
	cmd.Stderr = io.Discard
	// Same process-group handling as the TTS worker: its own group, killed as a
	// group on context cancellation. The package name is about who needed it
	// first, not about TTS.
	qwen.ConfigureWorkerCommand(cmd)

	client := &Client{
		cmd:         cmd,
		stdin:       stdin,
		events:      make(chan Event, eventBuffer),
		done:        make(chan struct{}),
		stopTimeout: cmp.Or(opts.StopTimeout, DefaultStopTimeout),
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start talk hotkey helper: %w", err)
	}

	var readers sync.WaitGroup
	readers.Go(func() { client.scan(stdout) })
	go func() {
		readers.Wait()
		_ = cmd.Wait()
		_ = stdin.Close()
		close(client.events)
		close(client.done)
	}()
	return client, nil
}

// Events streams what the helper reported. It closes after the helper exits.
func (c *Client) Events() <-chan Event { return c.events }

// Done closes when the helper has exited and Events is drained of new sends.
func (c *Client) Done() <-chan struct{} { return c.done }

// Stop asks the helper to leave, escalates to a kill after StopTimeout, and
// returns once it is gone. Safe to call more than once and from more than one
// goroutine.
func (c *Client) Stop() {
	c.stopOnce.Do(func() {
		if c.cmd.Process != nil {
			_ = c.cmd.Process.Signal(os.Interrupt)
		}
		select {
		case <-c.done:
		case <-time.After(c.stopTimeout):
			if c.cmd.Process != nil {
				_ = c.cmd.Process.Kill()
			}
		}
	})
	<-c.done
}

func (c *Client) scan(reader io.Reader) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4096), 1024*1024)
	for scanner.Scan() {
		if event, ok := ParseEvent(scanner.Bytes()); ok {
			c.emit(event)
		}
	}
}

func (c *Client) emit(event Event) {
	select {
	case c.events <- event:
	default:
	}
}
