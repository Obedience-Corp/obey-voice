package netapi

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/discussion"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

func TestParticipationControlValidationAndAcknowledgment(t *testing.T) {
	for _, tc := range []struct {
		name string
		msg  controlMessage
		fail bool
	}{
		{"all", controlMessage{RequestID: "request", Mode: "open", Members: json.RawMessage(`null`)}, false},
		{"subset", controlMessage{RequestID: "request", Mode: "invited", Members: json.RawMessage(`["jarvis"]`)}, false},
		{"missing members", controlMessage{RequestID: "request", Mode: "open"}, true},
		{"missing correlation", controlMessage{Mode: "open", Members: json.RawMessage(`null`)}, true},
		{"bad mode", controlMessage{RequestID: "request", Mode: "always", Members: json.RawMessage(`null`)}, true},
		{"empty roster", controlMessage{RequestID: "request", Mode: "open", Members: json.RawMessage(`[]`)}, true},
		{"duplicates", controlMessage{RequestID: "request", Mode: "open", Members: json.RawMessage(`["jarvis","jarvis"]`)}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bus := events.NewBus()
			d := NewDispatcher(snapshotTestRunner{}, bus, nil, nil)
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			go d.Run(ctx)
			called := false
			s := &Server{dispatcher: d, hub: newHub(), opts: Options{Bus: bus, SetParticipation: func(_ context.Context, settings discussion.Settings) error { called = true; return settings.Validate() }}}
			conn := &streamConn{out: make(chan []byte, connQueueDepth), kick: make(chan string, 1)}
			s.handleSetParticipation(ctx, conn, tc.msg)
			var frame struct {
				Type, Mode string
				RequestID  string              `json:"request_id"`
				Revision   uint64              `json:"state_revision"`
				Settings   discussion.Settings `json:"participation"`
			}
			select {
			case data := <-conn.out:
				if err := json.Unmarshal(data, &frame); err != nil {
					t.Fatal(err)
				}
			case <-ctx.Done():
				t.Fatal("no control response")
			}
			if tc.fail {
				if called || frame.Type != "error" {
					t.Fatalf("invalid request mutated state: %+v", frame)
				}
				return
			}
			if !called || frame.Type != "participation_set" || frame.RequestID != "request" || frame.Revision != d.StateRevision() || frame.Revision < 3 || frame.Settings.Mode != tc.msg.Mode {
				t.Fatalf("bad acknowledgment: %+v", frame)
			}
		})
	}
}

func TestParticipationCanceledControlCannotMutateLater(t *testing.T) {
	d := NewDispatcher(snapshotTestRunner{}, events.NewBus(), nil, nil)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	err := d.Control(ctx, func(context.Context) error { t.Fatal("canceled update applied"); return nil })
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	runCtx, stop := context.WithCancel(t.Context())
	defer stop()
	go d.Run(runCtx)
	if err := d.Control(t.Context(), func(context.Context) error { return nil }); err != nil {
		t.Fatal(err)
	}
}

func TestDiscussionEventHasScopeAndPublicIdentity(t *testing.T) {
	data, err := marshalEvent(events.DiscussionUpdated{SessionID: "session", ConversationEpoch: "epoch", Snapshot: discussion.Snapshot{ID: "discussion", Revision: 3, Phase: "checking", Members: []string{"jarvis"}, Candidate: "jarvis", Labels: map[string]string{"jarvis": "Jarvis"}}})
	if err != nil {
		t.Fatal(err)
	}
	var frame map[string]any
	if err := json.Unmarshal(data, &frame); err != nil {
		t.Fatal(err)
	}
	if frame["type"] != "discussion_updated" || frame["candidate"] != "jarvis" || frame["conversation_epoch"] != "epoch" || frame["discussion_revision"] != float64(3) {
		t.Fatalf("incomplete event: %s", data)
	}
}
