//go:build !integration

package cmd

import (
	"bufio"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

// The endpoint is local to the user and install. The port distinguishes serve
// instances without relying on a PID that might be reused by another process.
func serveControlPath(dir string, port int) string {
	sum := sha256.Sum256([]byte(dir))
	// A fixed location lets a desktop-launched server and an interactive shell
	// find each other even when their TMPDIR values differ.
	root := filepath.Join("/tmp", fmt.Sprintf("obey-voice-%d", os.Getuid()))
	return filepath.Join(root, fmt.Sprintf("serve-%x-%d.sock", sum[:8], port))
}

type serveController struct {
	listener net.Listener
	mu       sync.Mutex
	action   string
	cancel   context.CancelFunc
}

func listenServeControl(dir string, port int) (*serveController, error) {
	path := serveControlPath(dir, port)
	if runtime.GOOS == "windows" {
		return nil, errors.New("serve lifecycle control is not supported on Windows")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create serve control directory: %w", err)
	}
	info, err := os.Lstat(filepath.Dir(path))
	if err != nil || !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("serve control directory %s must be private", filepath.Dir(path))
	}
	lock, err := lockServeControl(path + ".lock")
	if err != nil {
		return nil, fmt.Errorf("a serve instance may already be running on port %d: %w", port, err)
	}
	keepLock := false
	defer func() {
		if !keepLock {
			_ = lock.Close()
		}
	}()
	listener, err := net.Listen("unix", path)
	if err != nil {
		conn, dialErr := net.DialTimeout("unix", path, 200*time.Millisecond)
		if dialErr == nil {
			_ = conn.Close()
			return nil, fmt.Errorf("a serve instance is already running on port %d", port)
		}
		info, statErr := os.Lstat(path)
		if statErr != nil || info.Mode()&os.ModeSocket == 0 {
			return nil, fmt.Errorf("listen for serve control: %w", err)
		}
		if removeErr := os.Remove(path); removeErr != nil {
			return nil, fmt.Errorf("remove stale serve control socket: %w", removeErr)
		}
		listener, err = net.Listen("unix", path)
		if err != nil {
			return nil, fmt.Errorf("listen for serve control: %w", err)
		}
	}
	keepLock = true
	c := &serveController{listener: &lockedServeListener{Listener: listener, lock: lock}}
	go c.accept()
	return c, nil
}

type lockedServeListener struct {
	net.Listener
	lock io.Closer
	once sync.Once
}

func (l *lockedServeListener) Close() error {
	var err error
	l.once.Do(func() {
		err = l.Listener.Close()
		if lockErr := l.lock.Close(); err == nil {
			err = lockErr
		}
	})
	return err
}

func (c *serveController) accept() {
	for {
		conn, err := c.listener.Accept()
		if err != nil {
			return
		}
		go c.handle(conn)
	}
}

func (c *serveController) handle(conn net.Conn) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(time.Second))
	line, err := bufio.NewReader(io.LimitReader(conn, 32)).ReadString('\n')
	if err != nil {
		return
	}
	action := strings.TrimSpace(line)
	if action != "stop" && action != "restart" {
		_, _ = fmt.Fprintln(conn, "invalid request")
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.action != "" {
		_, _ = fmt.Fprintln(conn, "another request is in progress")
		return
	}
	c.action = action
	_, _ = fmt.Fprintln(conn, "ok")
	if c.cancel != nil {
		c.cancel()
	}
}

func (c *serveController) bind(cancel context.CancelFunc) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cancel = cancel
	if c.action != "" {
		cancel()
	}
}

func (c *serveController) takeAction() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	action := c.action
	c.action = ""
	c.cancel = nil
	return action
}

func requestServeControl(dir string, port int, action string) error {
	if runtime.GOOS == "windows" {
		return errors.New("serve lifecycle control is not supported on Windows")
	}
	path := serveControlPath(dir, port)
	conn, err := net.DialTimeout("unix", path, time.Second)
	if err != nil {
		return fmt.Errorf("no controllable serve instance on port %d: %w", port, err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(time.Second))
	if _, err := fmt.Fprintln(conn, action); err != nil {
		return fmt.Errorf("send %s request: %w", action, err)
	}
	line, err := bufio.NewReader(io.LimitReader(conn, 256)).ReadString('\n')
	if err != nil {
		return fmt.Errorf("read %s response: %w", action, err)
	}
	if strings.TrimSpace(line) != "ok" {
		return fmt.Errorf("serve %s: %s", action, strings.TrimSpace(line))
	}
	return nil
}

func serveControlCmd(action string) *cobra.Command {
	port := defaultServePort
	cmd := &cobra.Command{
		Use: action, Short: strings.ToUpper(action[:1]) + action[1:] + " a local serve instance",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := requestServeControl(config.ConfigDir(), port, action); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "  %s requested for serve on port %d.\n", action, port)
			return nil
		},
	}
	cmd.Flags().IntVar(&port, "port", defaultServePort, "Port of the serve instance")
	return cmd
}

func runManagedServe(cmd *cobra.Command) error {
	if runtime.GOOS == "windows" {
		ctx, cancel := signalContext()
		defer cancel()
		return runServeOnce(ctx, cmd, &serveRestartState{})
	}
	control, err := listenServeControl(config.ConfigDir(), servePort)
	if err != nil {
		return err
	}
	defer control.listener.Close()
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("get startup directory: %w", err)
	}
	ctx, cancel := signalContext()
	defer cancel()
	state := &serveRestartState{}
	return serveControlLoop(ctx, cwd, control, func(ctx context.Context) error {
		state.saveErr = nil
		err := runServeOnce(ctx, cmd, state)
		if state.saveErr != nil {
			return fmt.Errorf("save conversation before stopping: %w", state.saveErr)
		}
		return err
	})
}

func serveControlLoop(ctx context.Context, cwd string, control *serveController, run func(context.Context) error) error {
	for {
		if err := os.Chdir(cwd); err != nil {
			return fmt.Errorf("restore startup directory: %w", err)
		}
		runCtx, stop := context.WithCancel(ctx)
		control.bind(stop)
		var err error
		if runCtx.Err() == nil {
			err = run(runCtx)
		}
		stop()
		action := control.takeAction()
		if action == "stop" && errors.Is(err, context.Canceled) {
			return nil
		}
		if action != "restart" || ctx.Err() != nil {
			return err
		}
		if err != nil && !errors.Is(err, context.Canceled) {
			return err
		}
		fmt.Fprintln(serveHumanOut, "  Restarting serve with its original options...")
	}
}
