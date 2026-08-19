package netapi

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

// Test deadlines. Short enough to run, long enough that a loaded CI box does
// not mistake scheduling jitter for a stalled client.
const (
	testSilence = 150 * time.Millisecond
	testTurnMax = 10 * time.Second
)

func shortDeadlines(o *Options) {
	o.VoiceSilenceTimeout = testSilence
	o.VoiceTurnMax = testTurnMax
}

// interruptLog records the reason of every turn_interrupted the bus carries,
// so a test can assert one was emitted — and, just as important, that a second
// one was not.
type interruptLog struct {
	mu      sync.Mutex
	reasons []string
}

func recordInterrupts(s *Server) *interruptLog {
	log := &interruptLog{}
	events.Subscribe(s.opts.Bus, func(e events.TurnInterrupted) {
		log.mu.Lock()
		defer log.mu.Unlock()
		log.reasons = append(log.reasons, e.Reason)
	})
	return log
}

func (l *interruptLog) all() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.reasons...)
}

func (l *interruptLog) count() int { return len(l.all()) }

// TestSilentVoiceTurnReleasesTheClaim is the failure that matters: a client
// sends voice_start, its socket dies without ever streaming a frame or sending
// voice_end, and the exclusive mic used to stay claimed for the life of the
// process. Every later turn then queued behind a turn nobody would ever end.
func TestSilentVoiceTurnReleasesTheClaim(t *testing.T) {
	runner := &voiceRunner{block: true, voiceStart: make(chan struct{}, 1)}
	s, d, addr, creds := startVoiceServerWith(t, runner, shortDeadlines)
	log := recordInterrupts(s)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	conn := dialStream(t, ctx, addr, creds)
	writeControl(t, ctx, conn, controlMessage{Type: "voice_start"})
	select {
	case <-runner.voiceStart:
	case <-ctx.Done():
		t.Fatal("voice turn never started")
	}
	eventually(t, "the live claim", func() bool { return micClaimed(s.hub) })

	eventually(t, "the mic back after the client went silent", func() bool {
		return !micClaimed(s.hub)
	})
	eventually(t, "the stalled turn to unwind", func() bool { return !d.TurnActive() })

	if got := log.all(); len(got) != 1 || got[0] != voiceSilentReason {
		t.Fatalf("turn_interrupted reasons = %v, want exactly one %q", got, voiceSilentReason)
	}
	// The deadline is a one-shot: a disarmed watchdog must not keep firing at
	// a connection that no longer has a turn.
	time.Sleep(3 * testSilence)
	if got := log.count(); got != 1 {
		t.Errorf("turn_interrupted emitted %d times, want exactly 1", got)
	}
}

// TestVoiceTurnDeadlineDoesNotCutALiveClient covers the other side, where a
// mistake is worse: cutting someone off mid-sentence. Streaming refreshes the
// deadline, and voice_end retires it.
func TestVoiceTurnDeadlineDoesNotCutALiveClient(t *testing.T) {
	tests := []struct {
		name string
		// act drives one connection past several silence windows.
		act       func(t *testing.T, ctx context.Context, ws *websocket.Conn)
		wantClaim bool
	}{
		{
			name: "frames keep refreshing the deadline",
			act: func(t *testing.T, ctx context.Context, ws *websocket.Conn) {
				for i := 0; i < 12; i++ {
					writeControl(t, ctx, ws, controlMessage{
						Type:       "audio_input",
						Data:       pcmFrame(160),
						SampleRate: 16000,
					})
					time.Sleep(testSilence / 3)
				}
			},
			wantClaim: true,
		},
		{
			name: "voice_end retires the deadline",
			act: func(t *testing.T, ctx context.Context, ws *websocket.Conn) {
				writeControl(t, ctx, ws, controlMessage{Type: "voice_end"})
				time.Sleep(4 * testSilence)
			},
			wantClaim: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner := &voiceRunner{block: true, voiceStart: make(chan struct{}, 1)}
			s, _, addr, creds := startVoiceServerWith(t, runner, shortDeadlines)
			log := recordInterrupts(s)

			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			conn := dialStream(t, ctx, addr, creds)
			frames := readFrames(t, ctx, conn)
			writeControl(t, ctx, conn, controlMessage{Type: "voice_start"})
			<-runner.voiceStart
			eventually(t, "the live claim", func() bool { return micClaimed(s.hub) })

			tt.act(t, ctx, conn)

			if got := micClaimed(s.hub); got != tt.wantClaim {
				t.Errorf("mic claimed = %v, want %v", got, tt.wantClaim)
			}
			if got := log.all(); len(got) != 0 {
				t.Errorf("turn_interrupted reasons = %v, want none", got)
			}
			if got := frames.await("error", 50*time.Millisecond); contains(got, "error") {
				t.Errorf("the connection was sent an error; frames = %v", got)
			}
		})
	}
}

// TestSilentQueuedVoiceTurnIsAbandoned: a queued turn holds no claim, but it
// still holds a place in the queue and an utterance the pipeline would run for
// a client that has stopped talking to us. The deadline drops it.
func TestSilentQueuedVoiceTurnIsAbandoned(t *testing.T) {
	runner := &voiceRunner{block: true, textStart: make(chan struct{}, 1)}
	s, _, addr, creds := startVoiceServerWith(t, runner, shortDeadlines)
	log := recordInterrupts(s)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	a := dialStream(t, ctx, addr, creds)
	b := dialStream(t, ctx, addr, creds)

	writeControl(t, ctx, a, controlMessage{Type: "text_input", Text: "hold the line"})
	<-runner.textStart

	// B queues a turn, streams one frame, then goes quiet for good.
	writeControl(t, ctx, b, controlMessage{Type: "voice_start"})
	writeControl(t, ctx, b, controlMessage{Type: "audio_input", Data: pcmFrame(160), SampleRate: 16000})
	eventually(t, "the queued turn to be abandoned", func() bool {
		return len(log.all()) == 1
	})
	if got := log.all(); got[0] != voiceSilentReason {
		t.Fatalf("turn_interrupted reason = %q, want %q", got[0], voiceSilentReason)
	}

	// Freeing the pipeline must not hand the mic to the turn that was dropped.
	writeControl(t, ctx, a, controlMessage{Type: "interrupt"})
	consistently(t, "the mic unclaimed after the abandoned turn was dequeued", func() bool {
		return !micClaimed(s.hub)
	})
	if got := runner.voiceTurns(); got != 0 {
		t.Errorf("voice turns run = %d, want 0 — an abandoned turn ran", got)
	}
}

// TestVoiceTurnDeadlineDisarmedOnDisconnect: a socket that really does close
// releases everything in handleStream's defer, and the deadline must go with
// it. Otherwise every disconnect would fire a turn_interrupted at the clients
// that stayed, for a turn that ended cleanly.
func TestVoiceTurnDeadlineDisarmedOnDisconnect(t *testing.T) {
	runner := &voiceRunner{block: true, voiceStart: make(chan struct{}, 1)}
	s, _, addr, creds := startVoiceServerWith(t, runner, shortDeadlines)
	log := recordInterrupts(s)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	conn := dialStream(t, ctx, addr, creds)
	writeControl(t, ctx, conn, controlMessage{Type: "voice_start"})
	<-runner.voiceStart
	eventually(t, "the live claim", func() bool { return micClaimed(s.hub) })

	conn.Close(websocket.StatusNormalClosure, "gone")
	eventually(t, "the claim released by the disconnect", func() bool { return !micClaimed(s.hub) })

	time.Sleep(3 * testSilence)
	if got := log.all(); len(got) != 0 {
		t.Errorf("turn_interrupted reasons = %v, want none: the disconnect already ended the turn", got)
	}
}

// TestVoiceTurnStalledUnderCancelledContext is the teardown case. The turn's
// context is already dead when the deadline fires — serve is shutting down, or
// the turn was cancelled out from under it — and the release path must still
// end cleanly: mic back, one event, nothing blocked.
func TestVoiceTurnStalledUnderCancelledContext(t *testing.T) {
	bus := events.NewBus()
	runner := &voiceRunner{block: true}
	d := NewDispatcher(runner, bus, nil, nil)

	ctx, cancel := context.WithCancel(context.Background())
	dispatchDone := make(chan struct{})
	go func() {
		defer close(dispatchDone)
		d.Run(ctx)
	}()

	ingress := audio.NewIngress()
	t.Cleanup(func() { ingress.Close() })

	s := New(Options{Bus: bus, Dispatcher: d, Ingress: ingress})
	log := recordInterrupts(s)

	conn := newTestConn(connQueueDepth, audioQueueDepth)
	if !s.hub.add(conn) {
		t.Fatal("hub refused the connection")
	}
	if err := s.hub.claimMic(conn); err != nil {
		t.Fatalf("claimMic: %v", err)
	}

	cancel()
	select {
	case <-dispatchDone:
	case <-time.After(3 * time.Second):
		t.Fatal("dispatcher did not stop on context cancellation")
	}

	s.voiceTurnStalled(conn)

	if micClaimed(s.hub) {
		t.Error("the claim survived the deadline under a cancelled context")
	}
	if !ingress.Finalized() {
		t.Error("the utterance was left open, so the recogniser would still block on frames")
	}
	if got := log.all(); len(got) != 1 || got[0] != voiceSilentReason {
		t.Errorf("turn_interrupted reasons = %v, want exactly one %q", got, voiceSilentReason)
	}
}

// TestVoiceWatchdogDeadlines pins the timer itself, away from sockets: it fires
// once, a frame defers it, disarming retires it, and the turn cap is not
// something frames can push out.
func TestVoiceWatchdogDeadlines(t *testing.T) {
	const quick = 20 * time.Millisecond

	tests := []struct {
		name      string
		silence   time.Duration
		max       time.Duration
		after     func(w *voiceWatchdog)
		wantFires int
	}{
		{
			name:      "disarmed before the deadline",
			silence:   quick,
			max:       time.Hour,
			after:     func(w *voiceWatchdog) { w.disarm() },
			wantFires: 0,
		},
		{
			name:      "silent turn fires once",
			silence:   quick,
			max:       time.Hour,
			after:     func(w *voiceWatchdog) {},
			wantFires: 1,
		},
		{
			name:    "frames defer the silence deadline",
			silence: 10 * quick,
			max:     time.Hour,
			after: func(w *voiceWatchdog) {
				for i := 0; i < 10; i++ {
					w.refresh()
					time.Sleep(quick)
				}
			},
			wantFires: 0,
		},
		{
			name:    "frames cannot defer the turn cap",
			silence: time.Hour,
			max:     quick,
			after: func(w *voiceWatchdog) {
				for i := 0; i < 10; i++ {
					w.refresh()
					time.Sleep(quick / 2)
				}
			},
			wantFires: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fired := make(chan struct{}, 8)

			var w voiceWatchdog
			w.arm(tt.silence, tt.max, func() { fired <- struct{}{} })
			tt.after(&w)

			if tt.wantFires == 0 {
				time.Sleep(4 * quick)
				if got := len(fired); got != 0 {
					t.Fatalf("watchdog fired %d times, want 0", got)
				}
				return
			}
			select {
			case <-fired:
			case <-time.After(3 * time.Second):
				t.Fatal("watchdog never fired")
			}
			// One turn, one release: a second fire would release a claim the
			// next turn had already been granted.
			time.Sleep(4 * quick)
			if got := len(fired); got != 0 {
				t.Errorf("watchdog fired %d extra times, want 0", got)
			}
		})
	}
}
