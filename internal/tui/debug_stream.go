package tui

import (
	"context"
	"path/filepath"
	"sync"
	"time"

	"github.com/Obedience-Corp/obey-voice/internal/debugstream"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

// withDebugStream attaches only to the actual runtime returned by the TUI
// builder. Its owner includes lost-ready builds, /menu, outer quit and errors.
func withDebugStream(build RuntimeBuilder) RuntimeBuilder {
	return func(ctx context.Context, progress BuildProgress, sessionID, personaID string) (*ConversationRuntime, error) {
		rt, err := build(ctx, progress, sessionID, personaID)
		if err != nil || rt == nil || rt.Pipeline == nil || rt.Bus == nil {
			return rt, err
		}
		trace := rt.Pipeline.CaptureTrace()
		if trace.RuntimeID == "" {
			return rt, nil
		}
		root := filepath.Join(config.ConfigDir(), "debug", "runtimes")
		server, startErr := debugstream.Start(root, rt.AgentName, rt.Bus, trace)
		if startErr != nil {
			rt.debugError = startErr
			return rt, nil
		}
		liveCtx, cancel := context.WithCancel(ctx)
		done := make(chan struct{})
		// Snapshot the label; UI persona changes must not race the lifecycle worker.
		label := rt.AgentName
		go func() {
			defer close(done)
			defer func() { server.Close() }()
			tick := time.NewTicker(250 * time.Millisecond)
			defer tick.Stop()
			for {
				select {
				case <-liveCtx.Done():
					return
				case <-tick.C:
					next := rt.Pipeline.CaptureTrace()
					if next.RuntimeID != trace.RuntimeID || next.ConversationEpoch != trace.ConversationEpoch || next.SessionID != trace.SessionID {
						server.Close()
						replacement, err := debugstream.Start(root, label, rt.Bus, next)
						if err != nil {
							continue
						}
						server, trace = replacement, next
					}
				}
			}
		}()
		cleanup := rt.Cleanup
		var once sync.Once
		rt.Cleanup = func() {
			once.Do(func() {
				cancel()
				<-done
				if cleanup != nil {
					cleanup()
				}
			})
		}
		return rt, nil
	}
}
