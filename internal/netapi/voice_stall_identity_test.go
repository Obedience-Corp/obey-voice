package netapi

import (
	"testing"

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
				conn.openStaging(gen)
				conn.queuedTurn.Store(gen)
				// …and it has since been dequeued: out of the queue, buffer
				// handed over, only its deadline still in flight.
				conn.queuedTurn.Store(0)
				conn.closeStaging()
				return gen
			},
			takeOver: func(t *testing.T, s *Server, conn *streamConn) {
				gen := s.armVoiceTurn(conn)
				conn.openStaging(gen)
				conn.queuedTurn.Store(gen)
				if !conn.stage(make([]float32, 160)) {
					t.Fatal("staging refused the frame")
				}
			},
			check: func(t *testing.T, s *Server, conn *streamConn) {
				if conn.queuedTurn.Load() == 0 {
					t.Error("the stalled turn abandoned the queued turn behind it")
				}
				staged, _ := conn.closeStaging()
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
