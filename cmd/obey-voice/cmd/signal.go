package cmd

import (
	"context"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"
)

var brokenPipeOnce sync.Once

// dropBrokenPipeSignals makes a write to a closed stdout or stderr return
// EPIPE instead of killing the process, which is what lets an orphaned serve
// outlive the app that spawned it.
//
// Notify with a drain, not signal.Ignore: Ignore installs SIG_IGN, and SIG_IGN
// is inherited across exec. serve spawns children that must keep the default
// disposition — native TTS/STT workers, the brain CLI, and tool shells where a
// `cmd | head` has to end when head exits. Notify changes only this process.
//
// Installed once and never removed: it is a property of a serve process, and
// nothing may write to a closed pipe in the window a removal would open.
func dropBrokenPipeSignals() {
	brokenPipeOnce.Do(func() {
		ch := make(chan os.Signal, 1)
		signal.Notify(ch, syscall.SIGPIPE)
		go func() {
			for range ch { // a broken pipe is a dropped write, nothing more
			}
		}()
	})
}

// forceQuitTimeout caps how long graceful shutdown may run after the first
// interrupt before the process force-exits.
const forceQuitTimeout = 3 * time.Second

// signalContext returns a context cancelled on the first SIGINT/SIGTERM. A
// second signal, or forceQuitTimeout elapsing, force-exits — so unlike
// signal.NotifyContext, a blocked shutdown can never leave the process ignoring
// interrupts.
func signalContext() (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())

	sigCh := make(chan os.Signal, 2)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)

	go func() {
		select {
		case <-sigCh:
			cancel()
		case <-ctx.Done(): // normal completion
			signal.Stop(sigCh)
			return
		}

		select {
		case <-sigCh: // second signal
		case <-time.After(forceQuitTimeout): // shutdown wedged
		}
		os.Exit(130)
	}()

	return ctx, cancel
}

// interruptContext is signalContext under parent, for work whose cleanup must
// not be cut short by the force-quit timer: only a second signal exits 130.
// The returned stop unregisters the handler.
func interruptContext(parent context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(parent)
	sigCh := make(chan os.Signal, 2)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	done := make(chan struct{})
	go func() {
		defer signal.Stop(sigCh)
		select {
		case <-sigCh:
			cancel()
		case <-done:
			return
		}
		select {
		case <-sigCh:
			os.Exit(130)
		case <-done:
		}
	}()
	var once sync.Once
	return ctx, func() {
		once.Do(func() {
			close(done)
			cancel()
		})
	}
}
