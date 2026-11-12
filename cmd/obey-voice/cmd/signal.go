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

// interruptExit ends the process on a second Ctrl-C; tests replace it.
var interruptExit = os.Exit

// interruptContext is cancelled by the first SIGINT, SIGTERM or SIGHUP, for
// work whose cleanup must not be cut short by a timer. Only a second SIGINT,
// someone at the keyboard insisting, exits 130; SIGTERM and SIGHUP, which a
// parent app or a closed terminal sends, never cut the cleanup short.
// onSignal, when set, runs on every signal before it takes effect. The
// returned stop unregisters the handler.
func interruptContext(parent context.Context, onSignal func(sig os.Signal, first bool)) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(parent)
	sigCh := make(chan os.Signal, 4)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	done := make(chan struct{})
	go func() {
		defer signal.Stop(sigCh)
		first := true
		for {
			select {
			case sig := <-sigCh:
				if onSignal != nil {
					onSignal(sig, first)
				}
				if !first && sig == os.Interrupt {
					interruptExit(130)
				}
				first = false
				cancel()
			case <-done:
				return
			}
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
