package main

import (
	"context"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
)

func TestObservingRecordsOptionsPerEntryPoint(t *testing.T) {
	o := &observing{inner: newStubWorker()}
	if _, err := o.ThinkFull(context.Background(), "task", brain.WorkerStreamOptions(true)); err != nil {
		t.Fatal(err)
	}
	stream, err := o.ThinkStream(context.Background(), "hi", brain.StreamOptions{RouterToolsPolicy: brain.RouterToolsRouteOnly})
	if err != nil {
		t.Fatal(err)
	}
	for range stream.Chunks {
	}
	<-stream.Done

	s, f := o.calls()
	if s != 1 || f != 1 {
		t.Fatalf("calls = (%d, %d), want (1, 1)", s, f)
	}
	if got := o.fullOptions(); !got.ToolsEnabled || got.RouterToolsPolicy != brain.RouterToolsLegacy {
		t.Fatalf("worker options = %+v, want legacy with tools", got)
	}
	if got := o.streamOptions(); got.RouterToolsPolicy != brain.RouterToolsRouteOnly {
		t.Fatalf("live options = %+v, want route_only", got)
	}
}

func TestLiveRunnerReportsCallsSinceTurnMark(t *testing.T) {
	live := &observing{inner: newStubWorker()}
	worker := &observing{inner: newStubWorker()}
	r := &liveRunner{live: live, worker: worker}

	r.mark()
	if r.liveCalled() || r.workerCalled() {
		t.Fatal("fresh mark must report no calls")
	}
	if _, err := worker.ThinkFull(context.Background(), "task", brain.WorkerStreamOptions(true)); err != nil {
		t.Fatal(err)
	}
	if r.liveCalled() {
		t.Fatal("worker call must not count as a live call")
	}
	if !r.workerCalled() {
		t.Fatal("worker call after the mark must be visible")
	}
	if r.workerPolicy() != string(brain.RouterToolsLegacy) {
		t.Fatalf("workerPolicy = %q", r.workerPolicy())
	}
	if tools := r.workerTools(); tools == nil || !*tools {
		t.Fatalf("workerTools = %v, want true", tools)
	}

	r.mark()
	if r.workerCalled() {
		t.Fatal("a new mark hides the previous turn's worker call")
	}
	if r.workerTools() != nil {
		t.Fatal("workerTools must be nil when the worker was not called this turn")
	}
}
