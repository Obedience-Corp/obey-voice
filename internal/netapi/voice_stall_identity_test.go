package netapi

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

// stalledTurnServer builds a server with a remote mic and no sockets: the test
// invokes the deadline callback itself rather than waiting one out.
func stalledTurnServer(t *testing.T) (*Server, *streamConn) {
	t.Helper()
	bus := events.NewBus()
	ingress := audio.NewIngress()
	t.Cleanup(func() { ingress.Close() })

	s := New(Options{Bus: bus, Dispatcher: NewDispatcher(&voiceRunner{}, bus, nil, nil), Ingress: ingress})
	conn := newTestConn(connQueueDepth, audioQueueDepth)
	if !s.hub.add(conn) {
		t.Fatal("hub refused the connection")
	}
	t.Cleanup(conn.watch.disarm)
	return s, conn
}

// TestStalledTurnCannotTouchTheNextTurn is the race expire leaves open: it
// drops its own lock before running the callback, and in that window the
// dispatcher can dequeue this connection's next turn, arm it and grant it the
// mic. The callback then arrives holding a turn that is over and must take
// nothing from the one that replaced it.
func TestStalledTurnCannotTouchTheNextTurn(t *testing.T) {
	tests := []struct {
		name string
		// stalled opens the turn the deadline fires for, and returns the
		// generation it was armed with.
		stalled func(t *testing.T, s *Server, conn *streamConn) uint64
		// takeOver opens the turn that replaced it, as the dispatch goroutine
		// does when it dequeues the next one.
		takeOver func(t *testing.T, s *Server, conn *streamConn)
		check    func(t *testing.T, s *Server, conn *streamConn)
	}{
		{
			name: "the claim the next turn was just granted",
			stalled: func(t *testing.T, s *Server, conn *streamConn) uint64 {
				gen := s.armVoiceTurn(conn)
				if err := s.hub.claimMic(conn, gen); err != nil {
					t.Fatalf("claimMic: %v", err)
				}
				return gen
			},
			takeOver: func(t *testing.T, s *Server, conn *streamConn) {
				if err := s.hub.claimMic(conn, s.armVoiceTurn(conn)); err != nil {
					t.Fatalf("claimMic: %v", err)
				}
			},
			check: func(t *testing.T, s *Server, conn *streamConn) {
				if !s.hub.isClaimant(conn) {
					t.Error("the stalled turn released the claim its successor had been granted")
				}
			},
		},
		{
			name: "the audio the next turn has staged",
			stalled: func(t *testing.T, s *Server, conn *streamConn) uint64 {
				gen := s.armVoiceTurn(conn)
				conn.openQueuedTurn(gen)
				// …and it has since been dequeued: out of the queue, buffer
				// handed over, only its deadline still in flight.
				conn.takeQueuedTurn()
				return gen
			},
			takeOver: func(t *testing.T, s *Server, conn *streamConn) {
				gen := s.armVoiceTurn(conn)
				conn.openQueuedTurn(gen)
				if !conn.stage(make([]float32, 160)) {
					t.Fatal("staging refused the frame")
				}
			},
			check: func(t *testing.T, s *Server, conn *streamConn) {
				staged, _, ok := conn.takeQueuedTurn()
				if !ok {
					t.Fatal("the stalled turn abandoned the queued turn behind it")
				}
				if len(staged) != 160 {
					t.Errorf("staged samples = %d, want 160: the stalled turn dropped its successor's audio", len(staged))
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, conn := stalledTurnServer(t)
			log := recordInterrupts(s)

			gen := tt.stalled(t, s, conn)
			tt.takeOver(t, s, conn)
			s.voiceTurnStalled(conn, gen, voiceSilentReason)

			tt.check(t, s, conn)
			if got := log.all(); len(got) != 0 {
				t.Errorf("bus turn_interrupted = %v, want none: the stalled turn was already over", got)
			}
			if got := len(conn.out); got != 0 {
				t.Errorf("%d events sent to the connection, want none: the turn that ended was not the one it is running", got)
			}
		})
	}
}

// drainIngress reads the utterance the recogniser would see: every sample up to
// and including the final frame.
func drainIngress(t *testing.T, ing *audio.Ingress) (samples int, final bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	for {
		frame, err := ing.ReadFrame(ctx)
		if err != nil {
			t.Fatalf("read ingress: %v", err)
		}
		samples += len(frame.Samples)
		if frame.Final {
			return samples, true
		}
	}
}

// nextEnvelope pops one envelope off a connection's outbound queue.
func nextEnvelope(t *testing.T, conn *streamConn) map[string]any {
	t.Helper()
	select {
	case msg := <-conn.out:
		var env map[string]any
		if err := json.Unmarshal(msg, &env); err != nil {
			t.Fatalf("outbound envelope is not JSON: %v", err)
		}
		return env
	default:
		t.Fatal("nothing was sent to the connection")
		return nil
	}
}

// TestQueuedTurnSurvivesAStallInFlight is the interleaving the review found. The
// deadline for a queued turn passes the watchdog's own checks and is on its way
// to the connection when the dispatcher dequeues that same turn. While the
// marker and the buffer were separate, the callback landed between them: the
// marker was already gone so it reported nothing, and it still emptied the
// buffer the promoted turn was about to replay. The turn then ran on silence,
// its utterance lost, and nobody was told anything.
func TestQueuedTurnSurvivesAStallInFlight(t *testing.T) {
	s, conn := stalledTurnServer(t)
	log := recordInterrupts(s)

	// A queued turn holding a complete utterance: the client spoke and sent
	// voice_end while the pipeline was busy.
	gen := s.armVoiceTurn(conn)
	conn.openQueuedTurn(gen)
	if !conn.stage(make([]float32, 160)) {
		t.Fatal("staging refused the frame")
	}
	if !conn.markStagedFinal() {
		t.Fatal("staging refused the final marker")
	}

	// The dequeue takes the turn…
	staged, final, ok := conn.takeQueuedTurn()
	if !ok {
		t.Fatal("the dequeue found no queued turn")
	}
	// …the deadline armed for that same turn lands here…
	s.voiceTurnStalled(conn, gen, voiceSilentReason)
	// …and the dequeue goes on to arm, claim and replay, as claimQueuedMic does.
	if len(staged) != 160 || !final {
		t.Fatalf("the dequeue took %d samples, final = %v; want 160 and true — the stalled turn emptied the buffer", len(staged), final)
	}
	next := s.armVoiceTurn(conn)
	if err := s.hub.claimMic(conn, next); err != nil {
		t.Fatalf("claimMic: %v", err)
	}
	if err := s.hub.writeMic(conn, staged); err != nil {
		t.Fatalf("writeMic: %v", err)
	}
	if err := s.hub.endMicUtterance(conn); err != nil {
		t.Fatalf("endMicUtterance: %v", err)
	}

	if got, final := drainIngress(t, s.hub.ingress); got != 160 || !final {
		t.Errorf("the recogniser received %d samples, final = %v; want 160 and true", got, final)
	}
	if !s.hub.isClaimant(conn) {
		t.Error("the promoted turn lost the claim it had just been granted")
	}
	if n := len(conn.out); n != 0 {
		t.Errorf("%d events sent to the owner, want none: its turn is running, not over", n)
	}
	if got := log.all(); len(got) != 0 {
		t.Errorf("bus turn_interrupted = %v, want none", got)
	}
}

// TestQueuedTurnAbandonedBeforeItDequeues is the mirror order: the deadline
// completes first, so the turn and its audio are gone and its owner has been
// told once. The dequeue that arrives afterwards must run nothing, claim
// nothing, and say nothing more.
func TestQueuedTurnAbandonedBeforeItDequeues(t *testing.T) {
	s, conn := stalledTurnServer(t)
	log := recordInterrupts(s)

	gen := s.armVoiceTurn(conn)
	conn.openQueuedTurn(gen)
	if !conn.stage(make([]float32, 160)) {
		t.Fatal("staging refused the frame")
	}

	s.voiceTurnStalled(conn, gen, voiceSilentReason)
	if s.claimQueuedMic(conn) {
		t.Error("an abandoned turn was promoted and took the mic")
	}

	env := nextEnvelope(t, conn)
	if env["type"] != "turn_interrupted" || env["reason"] != voiceSilentReason {
		t.Errorf("owner received %v, want turn_interrupted %q", env, voiceSilentReason)
	}
	if n := len(conn.out); n != 0 {
		t.Errorf("%d further events sent to the owner, want none", n)
	}
	if micClaimed(s.hub) {
		t.Error("the abandoned turn took the mic")
	}
	if got := log.all(); len(got) != 0 {
		t.Errorf("bus turn_interrupted = %v, want none: the turn never ran", got)
	}
}
