package decision

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

func TestControllerDisableCancelsAndFencesOldResult(t *testing.T) {
	var controller Controller
	entered, cancelled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	provider := evaluatorFunc(func(ctx context.Context, _ Request) (Response, error) {
		calls.Add(1)
		close(entered)
		<-ctx.Done()
		close(cancelled)
		<-release // models a slow provider that has not returned on cancellation
		return certain(Simple), nil
	})
	options := Options{Policy: WorkPolicy{Evaluator: provider}, Budget: time.Second, VoiceWork: true}
	if controller.Start(t.Context(), VoiceWork, sampleInput()) != nil {
		t.Fatal("zero value active")
	}
	controller.Configure(options)
	if controller.Start(t.Context(), TextWork, sampleInput()) != nil {
		t.Fatal("disabled point active")
	}
	attempt := controller.Start(t.Context(), VoiceWork, sampleInput())
	if attempt == nil {
		t.Fatal("enabled point bypassed")
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("no request")
	}
	controller.Close()
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("disable did not cancel")
	}
	controller.Configure(options)
	if controller.Start(t.Context(), VoiceWork, sampleInput()) != nil {
		t.Fatal("overlapping request across configuration")
	}
	close(release)
	select {
	case <-attempt.pending.done:
	case <-time.After(time.Second):
		t.Fatal("request did not finish")
	}
	if _, ready := attempt.Take(); ready {
		t.Fatal("old generation accepted")
	}
	if calls.Load() != 1 {
		t.Fatalf("calls=%d", calls.Load())
	}
}

func TestControllerReadyDoesNotExpireWithInferenceBudget(t *testing.T) {
	var controller Controller
	controller.Configure(Options{Policy: WorkPolicy{Evaluator: evaluatorFunc(func(context.Context, Request) (Response, error) { return certain(Simple), nil })}, Budget: time.Second, TextWork: true})
	defer controller.Close()
	attempt := controller.Start(t.Context(), TextWork, sampleInput())
	result := <-attempt.pending.done
	attempt.pending.done <- result
	got, ready := attempt.Take()
	if !ready || !got.Proposal.Accepted {
		t.Fatalf("ready=%t result=%+v", ready, got)
	}
	if _, ready := attempt.Take(); ready {
		t.Fatal("accepted twice")
	}
}

func TestControllerCancelBeforeEvaluatorReleasesSlot(t *testing.T) {
	var controller Controller
	controller.Configure(Options{Policy: WorkPolicy{Evaluator: evaluatorFunc(func(ctx context.Context, _ Request) (Response, error) { return Response{}, ctx.Err() })}, Budget: time.Second, TextWork: true})
	defer controller.Close()
	for range 100 {
		ctx, cancel := context.WithCancel(t.Context())
		attempt := controller.Start(ctx, TextWork, sampleInput())
		cancel()
		if attempt != nil {
			<-attempt.pending.done
			attempt.Close()
		}
		deadline := time.After(time.Second)
		for controller.InFlight() {
			select {
			case <-deadline:
				t.Fatal("cancelled attempt leaked slot")
			default:
				time.Sleep(time.Millisecond)
			}
		}
	}
}
