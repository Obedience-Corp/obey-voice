package main

import (
	"context"
	"errors"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

func TestRunRetainedTurnCancelsAfterWrongInput(t *testing.T) {
	bus := events.NewBus()
	started := make(chan struct{})
	ctx := context.Background()
	text, err, mismatch := runRetainedTurn(ctx, bus, "expected phrase", func(ctx context.Context) (string, error) {
		close(started)
		bus.Emit(events.UserInput{Text: "wrong phrase"})
		<-ctx.Done()
		return "", ctx.Err()
	})
	if err != nil || !mismatch || text != "wrong phrase" {
		t.Fatalf("guard result text=%q err=%v mismatch=%t", text, err, mismatch)
	}
	select {
	case <-started:
	default:
		t.Fatal("retained turn did not start")
	}
}

func TestRunRetainedTurnDoesNotCancelMatchingInput(t *testing.T) {
	bus := events.NewBus()
	text, err, mismatch := runRetainedTurn(context.Background(), bus, "expected phrase", func(ctx context.Context) (string, error) {
		bus.Emit(events.UserInput{Text: "Expected, phrase!"})
		return "final response", nil
	})
	if err != nil || mismatch || text != "final response" {
		t.Fatalf("guard result text=%q err=%v mismatch=%t", text, err, mismatch)
	}
}

func TestRunRetainedTurnPrefersSynchronousWrongInputOverCompletion(t *testing.T) {
	bus := events.NewBus()
	text, err, mismatch := runRetainedTurn(context.Background(), bus, "expected", func(context.Context) (string, error) {
		bus.Emit(events.UserInput{Text: "wrong"})
		return "provider answer", nil
	})
	if err != nil || !mismatch || text != "wrong" {
		t.Fatalf("guard result text=%q err=%v mismatch=%t", text, err, mismatch)
	}
}

func TestRunRetainedTurnPreservesErrorAndMissingInput(t *testing.T) {
	wantErr := errors.New("provider failed")
	text, err, mismatch := runRetainedTurn(context.Background(), events.NewBus(), "expected", func(context.Context) (string, error) {
		return "partial", wantErr
	})
	if !errors.Is(err, wantErr) || mismatch || text != "partial" {
		t.Fatalf("error result text=%q err=%v mismatch=%t", text, err, mismatch)
	}

	text, err, mismatch = runRetainedTurn(context.Background(), events.NewBus(), "expected", func(context.Context) (string, error) {
		return "final without user event", nil
	})
	if err != nil || mismatch || text != "final without user event" {
		t.Fatalf("missing-event result text=%q err=%v mismatch=%t", text, err, mismatch)
	}
}

func TestRunRetainedTurnMismatchJoinsBeforeReturning(t *testing.T) {
	bus := events.NewBus()
	joined := make(chan struct{})
	_, err, mismatch := runRetainedTurn(context.Background(), bus, "expected", func(ctx context.Context) (string, error) {
		bus.Emit(events.UserInput{Text: "wrong"})
		<-ctx.Done()
		close(joined)
		return "wrong", ctx.Err()
	})
	if err != nil || !mismatch {
		t.Fatalf("guard result err=%v mismatch=%t", err, mismatch)
	}
	select {
	case <-joined:
	default:
		t.Fatal("guard returned before retained turn joined")
	}
}
