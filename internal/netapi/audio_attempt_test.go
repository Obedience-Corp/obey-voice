package netapi

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/work"
)

func TestFanoutRevokedAttemptWritesNeitherWireFormat(t *testing.T) {
	scope := work.WorkScope{SessionID: "session", RuntimeEpoch: "runtime", ConversationEpoch: "conversation"}
	c := work.NewCoordinator(scope)
	permit, err := c.AcquireCapturePermit(scope, "capture", 7)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	ctx = audio.WithPlaybackAttempt(ctx, audio.PlaybackAttempt{ID: "old-attempt", CaptureEpoch: 7, Fence: permit})
	h := newHub()
	jsonConn, binaryConn := newTestConn(16, 16), newTestConn(16, 16)
	h.add(jsonConn)
	h.add(binaryConn)
	h.setConnAudio(jsonConn, true, false)
	h.setConnAudio(binaryConn, true, true)
	fanout := NewAudioFanout(nil)
	fanout.AttachHub(h)
	stream := bufferedPCM(t, ctx, 24000, []float32{0.5, -0.5, 0.25})
	if err := c.RevokeBinding(scope); err != nil {
		t.Fatal(err)
	}
	_, err = fanout.PlayStream(ctx, stream)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("revoked attempt error: %v", err)
	}
	if len(jsonConn.audio) != 0 || len(binaryConn.audio) != 0 || fanout.AcceptedCount() != 0 {
		t.Fatal("revoked attempt sent queued PCM")
	}
}

func TestFanoutResultRetainsAttemptIdentity(t *testing.T) {
	scope := work.WorkScope{SessionID: "session", RuntimeEpoch: "runtime", ConversationEpoch: "conversation"}
	c := work.NewCoordinator(scope)
	permit, err := c.AcquireCapturePermit(scope, "capture", 8)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	ctx = audio.WithPlaybackAttempt(ctx, audio.PlaybackAttempt{ID: "attempt", CaptureEpoch: 8, Fence: permit})
	fanout := NewAudioFanout(nil)
	stream := bufferedPCM(t, ctx, 24000, []float32{0.5, -0.5, 0.25})
	playback, err := fanout.PlayStream(ctx, stream)
	if err != nil {
		t.Fatal(err)
	}
	r := <-playback.Done()
	if r.AttemptID != "attempt" || r.CaptureEpoch != 8 {
		t.Fatalf("unattributed result: %+v", r)
	}
}
