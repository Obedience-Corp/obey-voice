package netapi

import (
	"context"
	"encoding/json"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/discussion"
)

func (s *Server) handleSetParticipation(ctx context.Context, conn *streamConn, msg controlMessage) {
	if s.opts.SetParticipation == nil {
		s.sendControlError(conn, msg.RequestID, "group participation is unavailable")
		return
	}
	if msg.RequestID == "" || len(msg.RequestID) > 128 || len(msg.Members) == 0 {
		s.sendControlError(conn, msg.RequestID, "set_participation requires request_id, mode and members (array or null)")
		return
	}
	settings := discussion.Settings{Version: discussion.Version, Mode: msg.Mode}
	if err := json.Unmarshal(msg.Members, &settings.Members); err != nil {
		s.sendControlError(conn, msg.RequestID, "invalid participation members")
		return
	}
	if err := settings.Validate(); err != nil {
		s.sendControlError(conn, msg.RequestID, err.Error())
		return
	}
	// A user changing participation takes priority over an autonomous discussion.
	s.interrupt()
	err := s.dispatcher.Control(ctx, func(ctx context.Context) error { return s.opts.SetParticipation(ctx, settings) })
	if err != nil {
		s.sendControlError(conn, msg.RequestID, err.Error())
		return
	}
	data, _ := json.Marshal(map[string]any{"type": "participation_set", "request_id": msg.RequestID, "participation": settings, "state_revision": s.dispatcher.StateRevision()})
	select {
	case conn.out <- data:
	default:
		conn.evict("client too slow")
	}
}
