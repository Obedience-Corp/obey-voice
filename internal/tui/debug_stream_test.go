package tui

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/internal/debugstream"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/pipeline"
	tea "github.com/charmbracelet/bubbletea"
)

func debugFixtureRuntime(t *testing.T) *ConversationRuntime {
	t.Helper()
	bus := events.NewBus()
	p := &pipeline.Pipeline{Events: bus, Brain: &stubBrain{}}
	if err := p.BeginConversationIdentity("debug-fixture"); err != nil {
		t.Fatal(err)
	}
	return &ConversationRuntime{Pipeline: p, Bus: bus, AgentName: "Obey · isolated fixture", Voice: false}
}
func waitDebugDescriptor(t *testing.T, root, previous string) debugstream.Descriptor {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		paths, _ := filepath.Glob(filepath.Join(root, "debug", "runtimes", "*.json"))
		for _, path := range paths {
			raw, _ := os.ReadFile(path)
			var d debugstream.Descriptor
			if json.Unmarshal(raw, &d) == nil && d.ID != previous {
				return d
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("live runtime never advertised")
	return debugstream.Descriptor{}
}
func TestDebugStreamFollowsActualTUIRuntimeWithRecorderOff(t *testing.T) {
	root := t.TempDir()
	config.SetConfigDirForTest(t, root)
	rt := debugFixtureRuntime(t)
	cleanups := 0
	rt.Cleanup = func() { cleanups++ }
	build := func(context.Context, BuildProgress, string, string) (*ConversationRuntime, error) { return rt, nil }
	_, err := runProgram(&config.Config{}, build, nil, true, func(app App) (tea.Model, error) {
		ready := buildRuntime(app.builder, app.runCtx, app.progress, app.slot, "", "")()
		model, _ := app.Update(ready)
		app = model.(App)
		if app.conversation.capture != nil || app.conversation.recordMode() != "" {
			t.Fatal("live debugging enabled recording")
		}
		first := waitDebugDescriptor(t, root, "")
		if first.RuntimeID != rt.Pipeline.CaptureTrace().RuntimeID {
			t.Fatal("attached to wrong runtime")
		}
		if err := rt.Pipeline.ClearConversation(); err != nil {
			t.Fatal(err)
		}
		next := waitDebugDescriptor(t, root, first.ID)
		if next.RuntimeID == first.RuntimeID {
			t.Fatal("clear retained stale clock identity")
		}
		if _, err := os.Stat(filepath.Join(root, "debug", "runtimes", first.ID+".json")); !os.IsNotExist(err) {
			t.Fatal("old runtime advertised")
		}
		return app, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if cleanups != 1 {
		t.Fatal("runtime cleanup count", cleanups)
	}
	paths, _ := filepath.Glob(filepath.Join(root, "debug", "runtimes", "*.json"))
	if len(paths) != 0 {
		t.Fatal("runtime survived outer quit", paths)
	}
	if _, err := os.Stat(filepath.Join(root, "captures")); !os.IsNotExist(err) {
		t.Fatal("live debugging wrote recordings")
	}
}

func TestDebugStreamLostReadyIsOwnedByRunCleanup(t *testing.T) {
	root := t.TempDir()
	config.SetConfigDirForTest(t, root)
	rt := debugFixtureRuntime(t)
	build := func(context.Context, BuildProgress, string, string) (*ConversationRuntime, error) { return rt, nil }
	_, err := runProgram(&config.Config{}, build, nil, true, func(app App) (tea.Model, error) {
		_ = buildRuntime(app.builder, app.runCtx, app.progress, app.slot, "", "")() // never delivered to Update
		waitDebugDescriptor(t, root, "")
		return app, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	paths, _ := filepath.Glob(filepath.Join(root, "debug", "runtimes", "*.json"))
	if len(paths) != 0 {
		t.Fatal("lost-ready build leaked endpoint")
	}
}
