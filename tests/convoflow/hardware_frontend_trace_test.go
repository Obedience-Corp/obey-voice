package main

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
)

type traceFrontendFake struct{ events []frontendTraceEvent }

func (f *traceFrontendFake) ProcessCapture(v []float32) []float32 {
	f.events = append(f.events, frontendTraceEvent{Op: "capture", Samples: append([]float32(nil), v...)})
	return v
}
func (f *traceFrontendFake) PushPlaybackReference(v []float32) {
	f.events = append(f.events, frontendTraceEvent{Op: "reference", Samples: append([]float32(nil), v...)})
}
func (f *traceFrontendFake) SetReferenceDelay(v int) {
	f.events = append(f.events, frontendTraceEvent{Op: "delay", Delay: v})
}
func (f *traceFrontendFake) Close() error {
	f.events = append(f.events, frontendTraceEvent{Op: "close"})
	return nil
}

var _ audio.Frontend = (*traceFrontendFake)(nil)
var _ audio.ReferenceDelayer = (*traceFrontendFake)(nil)

func TestRawCaptureFrontendTracePreservesOrderAndCopies(t *testing.T) {
	fake := &traceFrontendFake{}
	wrapped := newRawCaptureFrontend(fake)
	before := time.Now()
	wrapped.EnableTrace(true)
	capture := []float32{1, 2}
	ref := []float32{3, 4}
	wrapped.ProcessCapture(capture)
	wrapped.SetReferenceDelay(160)
	wrapped.PushPlaybackReference(ref)
	capture[0], ref[0] = 99, 98
	after := time.Now()
	path := t.TempDir() + "/trace.jsonl"
	if err := wrapped.WriteTrace(path); err != nil {
		t.Fatal(err)
	}
	trace := wrapped.TraceSnapshot()
	if len(trace) != 3 || trace[0].Seq != 1 || trace[1].Op != "delay" || trace[2].Op != "reference" {
		t.Fatalf("trace=%+v", trace)
	}
	if !reflect.DeepEqual(trace[0].Samples, []float32{1, 2}) || !reflect.DeepEqual(trace[2].Samples, []float32{3, 4}) {
		t.Fatalf("trace did not copy samples: %+v", trace)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	decoder := json.NewDecoder(file)
	var previousElapsed int64
	for _, expected := range trace {
		var persisted frontendTraceEvent
		if err := decoder.Decode(&persisted); err != nil {
			t.Fatal(err)
		}
		if persisted.ObservedAt.Before(before) || persisted.ObservedAt.After(after) ||
			!persisted.ObservedAt.Equal(expected.ObservedAt) || persisted.ElapsedNS != expected.ElapsedNS ||
			persisted.ElapsedNS < previousElapsed {
			t.Fatalf("lost frontend timing in persisted trace: %+v", persisted)
		}
		previousElapsed = persisted.ElapsedNS
	}
}

func TestReplayFrontendTraceReplaysOrderedData(t *testing.T) {
	fake := &traceFrontendFake{}
	wrapped := newRawCaptureFrontend(fake)
	wrapped.EnableTrace(true)
	wrapped.ProcessCapture([]float32{1})
	wrapped.SetReferenceDelay(80)
	wrapped.PushPlaybackReference([]float32{2})
	path := t.TempDir() + "/trace.jsonl"
	if err := wrapped.WriteTrace(path); err != nil {
		t.Fatal(err)
	}
	replayed := &traceFrontendFake{}
	if err := replayFrontendTrace(path, replayed); err != nil {
		t.Fatal(err)
	}
	want := []frontendTraceEvent{{Op: "capture", Samples: []float32{1}}, {Op: "delay", Delay: 80}, {Op: "reference", Samples: []float32{2}}}
	if !reflect.DeepEqual(replayed.events, want) {
		t.Fatalf("replayed=%+v want=%+v", replayed.events, want)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
}
