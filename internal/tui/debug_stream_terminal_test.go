package tui

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	tea "github.com/charmbracelet/bubbletea"
)

// Opt-in source for the debugger's cross-repository VHS harness. Uses the real
// TUI runtime owner/exporter with synthetic source timings; no providers/audio.
func TestLiveDebuggerTerminalSource(t *testing.T) {
	root := os.Getenv("OBEY_VOICE_LIVE_DEMO_ROOT")
	if root == "" {
		t.Skip("cross-repo terminal fixture")
	}
	config.SetConfigDirForTest(t, root)
	rt := debugFixtureRuntime(t)
	build := func(context.Context, BuildProgress, string, string) (*ConversationRuntime, error) { return rt, nil }
	_, err := runProgram(&config.Config{}, build, nil, true, func(app App) (tea.Model, error) {
		ready := buildRuntime(app.builder, app.runCtx, app.progress, app.slot, "", "")()
		model, _ := app.Update(ready)
		app = model.(App)
		deadline := time.After(70 * time.Second)
		for turn := 1; ; turn++ {
			emitDebugDemoTurn(rt, turn)
			select {
			case <-deadline:
				return app, nil
			case <-time.After(6 * time.Second):
			}
		}
	})
	if err != nil {
		t.Fatal(err)
	}
}

func emitDebugDemoTurn(rt *ConversationRuntime, n int) {
	trace := rt.Pipeline.CaptureTrace()
	turn := fmt.Sprintf("turn-%d", n)
	response := fmt.Sprintf("reply-%d", n)
	persona := "Obey"
	base := float64(n * 3000)
	trace.TurnID = &turn
	trace.ResponseID = &response
	trace.PersonaID = &persona
	trace.TurnStartMS = &base
	trace.MonoMS = &base
	rt.Bus.Emit(events.PipelineTrace{Trace: &trace, Span: events.TraceSpan{ID: turn, Stage: "turn", Lane: "turn", State: "start", Status: "running", StartMS: &base}})
	rt.Bus.Emit(events.UserInput{Text: "Where is this conversation spending time?", Trace: &trace})
	span := func(id, stage string, start, end float64, segment *int) {
		a, b := base+start, base+end
		current := trace
		current.MonoMS = &b
		spanID := fmt.Sprintf("%s-%s", turn, id)
		if stage == "turn" {
			spanID = turn
		}
		rt.Bus.Emit(events.PipelineTrace{Trace: &current, Span: events.TraceSpan{ID: spanID, Stage: stage, Lane: stage, State: "end", Status: "completed", StartMS: &a, EndMS: &b, SegmentIndex: segment}})
	}
	span("stt", "stt", -120, 0, nil)
	span("admit", "admission", 0, 25, nil)
	a := base + 25
	running := trace
	running.MonoMS = &a
	rt.Bus.Emit(events.PipelineTrace{Trace: &running, Span: events.TraceSpan{ID: turn + "-model", Stage: "model", Lane: "model", State: "start", Status: "running", StartMS: &a}})
	for _, text := range []string{"The model starts quickly. ", "Synthesis overlaps playback, ", "but the second segment waits for text."} {
		rt.Bus.Emit(events.ResponseDelta{Text: text, Trace: &trace})
		time.Sleep(250 * time.Millisecond)
	}
	span("model", "model", 25, 800, nil)
	zero, one := 0, 1
	span("queue-0", "segment_queue", 180, 220, &zero)
	span("synth-0", "synthesis", 220, 480, &zero)
	span("play-0", "playback", 480, 1200, &zero)
	span("queue-1", "segment_queue", 800, 820, &one)
	span("synth-1", "synthesis", 820, 1450, &one)
	span("play-1", "playback", 1450, 2100, &one)
	span("turn", "turn", 0, 2100, nil)
	rt.Bus.Emit(events.ResponseReady{Response: "Synthesis of segment 1 overlaps playback of segment 0. The next playback starts at 1450ms, after a 250ms gap. Inspect the source intervals before choosing an optimization.", Trace: &trace})
}
