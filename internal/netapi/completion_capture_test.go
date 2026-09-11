package netapi

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/work"
)

func completionScope() work.WorkScope {
	return work.WorkScope{SessionID: "session", RuntimeEpoch: "runtime", ConversationEpoch: "conversation"}
}

func completionConn() *streamConn {
	return &streamConn{out: make(chan []byte, 4), audio: make(chan []byte, 4), kick: make(chan string, 1)}
}

func TestHubAcquireCompletionPermitWhenMicIdle(t *testing.T) {
	h := newHub()
	c := work.NewCoordinator(completionScope())
	p, err := h.acquireCompletion(context.Background(), c, "completion-1")
	if err != nil {
		t.Fatal(err)
	}
	if !p.Valid() {
		t.Fatal("idle completion permit is invalid")
	}
	h.completionMu.Lock()
	h.revokeCompletionLocked()
	h.completionMu.Unlock()
	if h.completionCoord != nil || h.completionPermit.Valid() {
		t.Fatal("completion binding remained installed")
	}
	if p.Valid() {
		t.Fatal("revoked idle permit remained valid")
	}
}

func TestHubCompletionPermitDeniedWhileMicClaimed(t *testing.T) {
	h := newHub()
	h.setIngress(audio.NewIngress())
	c := completionConn()
	if !h.add(c) {
		t.Fatal("failed to add test connection")
	}
	if err := h.claimMic(c, 1); err != nil {
		t.Fatal(err)
	}
	coord := work.NewCoordinator(completionScope())
	if _, err := h.acquireCompletion(context.Background(), coord, "completion"); !errors.Is(err, work.ErrConflict) {
		t.Fatalf("completion while mic claimed = %v", err)
	}
}

func TestVoiceStartRevokesCompletionBeforeAdmission(t *testing.T) {
	h := newHub()
	h.setIngress(audio.NewIngress())
	coord := work.NewCoordinator(completionScope())
	permit, err := h.acquireCompletion(context.Background(), coord, "completion")
	if err != nil {
		t.Fatal(err)
	}
	c := completionConn()
	if !h.add(c) {
		t.Fatal("failed to add test connection")
	}
	d := NewDispatcher(&scriptedRunner{}, events.NewBus(), nil, nil)
	s := &Server{dispatcher: d, hub: h}
	called := false
	if err := s.startMicTurn(c, func(prepare func() bool) error {
		called = true
		if permit.Valid() {
			t.Error("voice admission observed live completion permit")
		}
		if !prepare() {
			t.Error("immediate voice admission was abandoned")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !called || permit.Valid() {
		t.Fatalf("voice start did not revoke completion: called=%v valid=%v", called, permit.Valid())
	}
	h.releaseMic(c)
}

func TestCompletionRevokeAndAudioFenceDoNotDeadlock(t *testing.T) {
	h := newHub()
	h.setIngress(audio.NewIngress())
	coord := work.NewCoordinator(completionScope())
	permit, err := h.acquireCompletion(context.Background(), coord, "completion")
	if err != nil {
		t.Fatal(err)
	}
	doneWrite := make(chan struct{})
	doneRevoke := make(chan struct{})
	go func() {
		permit.Write(func() {
			h.broadcastAudio([]byte("pcm"))
		})
		close(doneWrite)
	}()
	go func() {
		h.completionMu.Lock()
		h.revokeCompletionLocked()
		h.completionMu.Unlock()
		close(doneRevoke)
	}()
	select {
	case <-doneWrite:
	case <-time.After(time.Second):
		t.Fatal("audio fence write deadlocked")
	}
	select {
	case <-doneRevoke:
	case <-time.After(time.Second):
		t.Fatal("completion revoke deadlocked with audio write")
	}
}

func TestCompletionAdmissionAndMicClaimAreMutuallyExclusive(t *testing.T) {
	h := newHub()
	h.setIngress(audio.NewIngress())
	c := completionConn()
	if !h.add(c) {
		t.Fatal("failed to add test connection")
	}
	coord := work.NewCoordinator(completionScope())
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	var permit work.CapturePermit
	var acquireErr, claimErr error
	go func() {
		defer wg.Done()
		<-start
		permit, acquireErr = h.acquireCompletion(context.Background(), coord, "completion")
	}()
	go func() {
		defer wg.Done()
		<-start
		claimErr = h.claimMic(c, 1)
	}()
	close(start)
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("completion admission and mic claim deadlocked")
	}
	if claimErr != nil {
		t.Fatal(claimErr)
	}
	if acquireErr != nil && !errors.Is(acquireErr, work.ErrConflict) {
		t.Fatal(acquireErr)
	}
	h.mu.Lock()
	claimed := h.micClaimant != nil
	h.mu.Unlock()
	h.completionMu.Lock()
	bound := h.completionCoord != nil && h.completionPermit.Valid()
	h.completionMu.Unlock()
	if !claimed || bound || permit.Valid() {
		t.Fatalf("mic/completion overlap: claimed=%v bound=%v permit=%v", claimed, bound, permit.Valid())
	}
}
