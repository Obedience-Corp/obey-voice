package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

type routingObservedFixture struct {
	*stubWorker
	calls int
}

func (f *routingObservedFixture) ClassifyLiveRoute(context.Context, brain.LiveRouteRequest) (brain.StreamResult, error) {
	f.calls++
	return brain.StreamResult{Route: "spawn"}, nil
}

func TestObservingPreservesHarnessRoutingCapability(t *testing.T) {
	inner := &routingObservedFixture{stubWorker: newStubWorker()}
	wrapped, observer := observeProvider(inner)
	router, ok := wrapped.(brain.LiveRouteClassifier)
	if !ok {
		t.Fatal("observer hid production harness routing")
	}
	result, err := router.ClassifyLiveRoute(t.Context(), brain.LiveRouteRequest{Text: "weather"})
	if err != nil || result.Route != "spawn" || inner.calls != 1 {
		t.Fatalf("routing=%+v calls=%d err=%v", result, inner.calls, err)
	}
	if stream, full := observer.calls(); stream != 0 || full != 0 {
		t.Fatal("private route inference counted as a talking/worker call")
	}
	ordinary, _ := observeProvider(newStubWorker())
	if _, ok := ordinary.(brain.LiveRouteClassifier); ok {
		t.Fatal("observer invented a routing capability")
	}
}

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

func TestObservingFreshOllamaWorkerPreservesCallsAndIsolation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/chat" {
			_, _ = io.WriteString(w, `{"message":{"role":"assistant","content":"Worker complete."},"done":true}`+"\n")
			return
		}
		_, _ = io.WriteString(w, `{"models":[{"name":"fixture"}]}`)
	}))
	defer server.Close()
	inner, err := brain.NewOllama(&config.Config{OllamaHost: server.URL, OllamaModel: "fixture", PromptsDir: t.TempDir(), WorkDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	inner.LoadHistory([]brain.Turn{{Role: "user", Content: "prior job context"}})
	wrapped, observed := observeProvider(inner)
	defer func() { _ = brain.CloseProvider(wrapped) }()
	fresh := wrapped.(brain.FreshProvider).Fresh()
	if len(fresh.History()) != 0 {
		t.Fatal("new worker inherited previous job history")
	}
	if _, err := fresh.ThinkFull(t.Context(), "finish the fixture", brain.WorkerStreamOptions(false)); err != nil {
		t.Fatal(err)
	}
	if _, calls := observed.calls(); calls != 1 {
		t.Fatalf("fresh worker call count = %d, want 1", calls)
	}
	if got := wrapped.History(); len(got) != 1 || got[0].Content != "prior job context" {
		t.Fatalf("fresh worker changed parent history: %+v", got)
	}
}
