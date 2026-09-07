package pipeline

import (
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

// collectToolEvents subscribes to the tool events a pipeline emits.
func collectToolEvents(bus *events.Bus, started *[]events.ToolCallStarted, finished *[]events.ToolCallFinished) {
	events.Subscribe(bus, func(e events.ToolCallStarted) { *started = append(*started, e) })
	events.Subscribe(bus, func(e events.ToolCallFinished) { *finished = append(*finished, e) })
}

func TestJobToolHooksAttributeToTheJob(t *testing.T) {
	bus := events.NewBus()
	var started []events.ToolCallStarted
	var finished []events.ToolCallFinished
	collectToolEvents(bus, &started, &finished)

	bumps := 0
	p := &Pipeline{Events: bus}
	p.brainDeadlineBump = func() { bumps++ }

	onStart, onEnd := p.jobToolHooks("job-7")
	call := brain.ToolCall{ID: "call_3", Name: "run_command", Kind: brain.KindExecute, Summary: "ls"}
	onStart(call)
	onEnd(brain.ToolResult{ToolCall: call, Preview: "a\nb", Err: "error: boom", Elapsed: 250 * time.Millisecond})

	if len(started) != 1 || len(finished) != 1 {
		t.Fatalf("emitted %d start / %d finish events, want one each", len(started), len(finished))
	}
	if started[0].JobID != "job-7" || finished[0].JobID != "job-7" {
		t.Errorf("job ids = %q / %q, want job-7", started[0].JobID, finished[0].JobID)
	}
	if started[0].Kind != string(brain.KindExecute) || finished[0].Kind != string(brain.KindExecute) {
		t.Errorf("kinds = %q / %q, want execute", started[0].Kind, finished[0].Kind)
	}
	if started[0].ID != "call_3" || finished[0].ID != "call_3" {
		t.Errorf("call ids = %q / %q, want call_3", started[0].ID, finished[0].ID)
	}
	if finished[0].Err != "error: boom" || finished[0].Elapsed != 250*time.Millisecond {
		t.Errorf("finish = %+v, want the error and elapsed carried through", finished[0])
	}
	if bumps != 0 {
		t.Errorf("brain deadline bumped %d times, want 0: worker tools must not extend the live turn watchdog", bumps)
	}
}

func TestLiveToolHooksKeepEmptyJobIDAndBump(t *testing.T) {
	bus := events.NewBus()
	var started []events.ToolCallStarted
	var finished []events.ToolCallFinished
	collectToolEvents(bus, &started, &finished)

	bumps := 0
	p := &Pipeline{Events: bus}
	p.brainDeadlineBump = func() { bumps++ }

	call := brain.ToolCall{ID: "route#1", Name: "route", Kind: brain.KindRoute, Summary: "spawn"}
	p.toolStartHook()(call)
	p.toolEndHook()(brain.ToolResult{ToolCall: call, Preview: "spawn"})

	if len(started) != 1 || len(finished) != 1 {
		t.Fatalf("emitted %d start / %d finish events, want one each", len(started), len(finished))
	}
	if started[0].JobID != "" || finished[0].JobID != "" {
		t.Errorf("job ids = %q / %q, want empty for the live brain", started[0].JobID, finished[0].JobID)
	}
	if bumps != 2 {
		t.Errorf("brain deadline bumped %d times, want 2", bumps)
	}
}
