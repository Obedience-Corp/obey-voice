package stt

import (
	"context"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/endpoint"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/work"
)

func pauseTestScope() work.WorkScope {
	return work.WorkScope{SessionID: "s", RuntimeEpoch: "r", ConversationEpoch: "c"}
}

func TestPauseDeniesActiveAndFinalizingSpeech(t *testing.T) {
	for name, state := range map[string]func(*pauseController){
		"speaking":   func(p *pauseController) { p.boundary(true, false, false) },
		"finalizing": func(p *pauseController) { p.boundary(false, true, false) },
	} {
		t.Run(name, func(t *testing.T) {
			p := newPauseController("test")
			resultDone := make(chan PauseResult, 1)
			go func() {
				result, _ := p.request(context.Background(), pauseTestScope(), name)
				resultDone <- result
			}()
			time.Sleep(time.Millisecond)
			state(p)
			result := <-resultDone
			if result.Decision == PauseAcknowledged {
				t.Fatal("active/finalizing capture was acknowledged")
			}
		})
	}
}

func TestPauseHasOnePendingClaimantAndResumeAfterRelease(t *testing.T) {
	scope := pauseTestScope()
	p := newPauseController("test")
	c := work.NewCoordinator(scope)
	p.BindCompletionCoordinator(c)
	firstDone := make(chan PauseResult, 1)
	go func() {
		result, _ := p.request(context.Background(), scope, "first")
		firstDone <- result
	}()
	time.Sleep(time.Millisecond)
	if _, err := p.request(context.Background(), scope, "second"); err == nil {
		t.Fatal("second claimant unexpectedly accepted")
	}
	_, _, _ = p.boundary(false, false, false)
	result := <-firstDone
	if result.Decision != PauseAcknowledged {
		t.Fatalf("first decision = %v", result.Decision)
	}
	if err := c.ReleaseCapturePermit(result.Token.Permit); err != nil {
		t.Fatal(err)
	}
	if err := p.resume(context.Background(), result.Token); err != nil {
		t.Fatalf("resume after normal release = %v", err)
	}
}

func TestPendingPauseResolvesWhenControllerStops(t *testing.T) {
	p := newPauseController("test")
	done := make(chan error, 1)
	go func() {
		_, err := p.request(context.Background(), pauseTestScope(), "pending")
		done <- err
	}()
	time.Sleep(time.Millisecond)
	p.stop()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("pending request resolved without close error")
		}
	case <-time.After(time.Second):
		t.Fatal("pending request remained blocked")
	}
}

func TestStopRevokesPermitAndRejectsNewPause(t *testing.T) {
	scope := pauseTestScope()
	p := newPauseController("test")
	p.BindCompletionCoordinator(work.NewCoordinator(scope))
	done := make(chan PauseResult, 1)
	go func() {
		result, _ := p.request(context.Background(), scope, "active")
		done <- result
	}()
	time.Sleep(time.Millisecond)
	_, _, _ = p.boundary(false, false, false)
	result := <-done
	if !result.Token.Permit.Valid() {
		t.Fatal("pause did not acquire permit")
	}
	p.stop()
	if result.Token.Permit.Valid() {
		t.Fatal("stop left capture permit valid")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := p.request(ctx, scope, "late"); err == nil {
		t.Fatal("new pause accepted after stop")
	}
}

func TestIdlePauseAcknowledgesBeforeListenTimeoutWithoutModel(t *testing.T) {
	scope := pauseTestScope()
	p := newPauseController("test")
	p.BindCompletionCoordinator(work.NewCoordinator(scope))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go runOfflineLoop(ctx, offlineLoopDeps{frames: noFrames{}, seg: &fakeSegmenter{}, pause: p, policy: endpoint.Policy{StartTimeout: 20 * time.Millisecond}}, make(chan Event, 8))
	requestCtx, requestCancel := context.WithTimeout(context.Background(), time.Second)
	defer requestCancel()
	result, err := p.request(requestCtx, scope, "idle")
	if err != nil || result.Decision != PauseAcknowledged {
		t.Fatalf("idle pause = %+v, %v", result, err)
	}
	cancel()
}
