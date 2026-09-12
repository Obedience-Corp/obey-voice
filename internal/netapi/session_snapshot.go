package netapi

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

// SessionSnapshot is the immutable current-session view sent when a client
// connects or a conversation boundary changes. Transcript is canonical
// history; it is never reconstructed from events received by the hub.
type SessionSnapshot struct {
	SessionID         string                  `json:"session_id"`
	ConversationEpoch string                  `json:"conversation_epoch"`
	Selection         events.PersonaSelection `json:"selection"`
	MicState          string                  `json:"mic_state"`
	StateRevision     uint64                  `json:"state_revision"`
	Transcript        []brain.Turn            `json:"transcript"`
}

func marshalSessionSnapshot(snapshot SessionSnapshot) ([]byte, error) {
	if snapshot.SessionID == "" || snapshot.ConversationEpoch == "" {
		return nil, errors.New("session snapshot is missing session identity")
	}
	if snapshot.Selection.SessionID != "" && snapshot.Selection.SessionID != snapshot.SessionID {
		return nil, errors.New("session snapshot selection belongs to another session")
	}
	if snapshot.Selection.ConversationEpoch != "" && snapshot.Selection.ConversationEpoch != snapshot.ConversationEpoch {
		return nil, errors.New("session snapshot selection belongs to another epoch")
	}
	return json.Marshal(struct {
		Type              string                  `json:"type"`
		SessionID         string                  `json:"session_id"`
		ConversationEpoch string                  `json:"conversation_epoch"`
		Selection         events.PersonaSelection `json:"selection"`
		MicState          string                  `json:"mic_state"`
		StateRevision     uint64                  `json:"state_revision"`
		Transcript        []brain.Turn            `json:"transcript"`
	}{
		Type: "session_snapshot", SessionID: snapshot.SessionID,
		ConversationEpoch: snapshot.ConversationEpoch, Selection: snapshot.Selection,
		MicState: snapshot.MicState, StateRevision: snapshot.StateRevision,
		Transcript: snapshot.Transcript,
	})
}

// SnapshotProvider returns one host-owned canonical state. It is called on
// the dispatcher goroutine, at a serialized boundary.
type SnapshotProvider func(context.Context) (SessionSnapshot, error)

var errTooManyStreamClients = errors.New("too many stream clients")

func (s *Server) installSnapshot(ctx context.Context, conn *streamConn, snapshot SessionSnapshot) error {
	s.hub.mu.Lock()
	defer s.hub.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	snapshot.MicState = s.hub.micStateLocked()
	if s.dispatcher != nil {
		snapshot.StateRevision = s.dispatcher.StateRevision()
	}
	data, err := marshalSessionSnapshot(snapshot)
	if err != nil {
		return err
	}
	if len(s.hub.conns) >= maxStreamClients {
		return errTooManyStreamClients
	}
	conn.audioBlocked.Store(s.hub.audioBlocked)
	s.hub.conns[conn] = struct{}{}
	select {
	case conn.out <- data:
		return nil
	default:
		delete(s.hub.conns, conn)
		return errors.New("session snapshot queue is full")
	}
}

// publishSnapshot is called by the dispatcher after clear/resume. The hub
// lock keeps the snapshot ordered with all subsequent event broadcasts.
func (s *Server) publishSnapshot(ctx context.Context) {
	if s.opts.CurrentSessionSnapshot == nil {
		return
	}
	snapshot, err := s.opts.CurrentSessionSnapshot(ctx)
	if err != nil {
		if s.opts.Bus != nil {
			s.opts.Bus.Emit(events.Error{Stage: "session_snapshot", Message: err.Error()})
		}
		return
	}
	s.hub.mu.Lock()
	snapshot.MicState = s.hub.micStateLocked()
	if s.dispatcher != nil {
		snapshot.StateRevision = s.dispatcher.StateRevision()
	}
	data, err := marshalSessionSnapshot(snapshot)
	if err == nil {
		for conn := range s.hub.conns {
			select {
			case conn.out <- data:
			default:
				conn.evict("client too slow")
				s.hub.removeLocked(conn)
			}
		}
	}
	s.hub.mu.Unlock()
}
