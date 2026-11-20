package tui

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/internal/testmedia"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/chatmedia"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/pipeline"
	tea "github.com/charmbracelet/bubbletea"
)

// An installed local model uses its direct tools in a disposable workspace.
func TestLiveDirectImageReachesTerminal(t *testing.T) {
	model := os.Getenv("OBEY_VOICE_IMAGE_ROUTING_MODEL")
	if model == "" {
		t.Skip("set OBEY_VOICE_IMAGE_ROUTING_MODEL to an installed local model")
	}
	root := t.TempDir()
	config.SetConfigDirForTest(t, filepath.Join(root, "config"))
	input := testmedia.PNG()
	if err := os.WriteFile(filepath.Join(root, "picture.png"), input, 0600); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{OllamaHost: "http://localhost:11434", OllamaModel: model, OllamaNumCtx: 32768, WorkDir: root, WorkerBoundary: root, PromptsDir: filepath.Join(root, "prompts"), SkillsDir: filepath.Join(root, "skills"), AgentName: "Obey", MaxHistory: 20, VoiceToolsEnabled: true, ContinuityIntegrityEnabled: true}
	live, err := brain.NewOllama(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	bus := events.NewBus()
	p := &pipeline.Pipeline{Brain: live, Events: bus, VoiceToolsEnabled: true, RouterToolsPolicy: brain.RouterToolsDirect}
	m := sizedConversation(t, 100, 42)
	m.cfg = cfg
	init := m.startConversation(conversationDeps{runner: p, ctx: ctx, bus: bus})
	m.input.SetValue("Show me a picture in this chat. Use the existing picture.png in the working directory.")
	h := &imageRepairTerminal{model: m, initial: init, wantImages: 1, stopOnTurnEnd: true}
	var output bytes.Buffer
	if _, err := tea.NewProgram(h, tea.WithContext(ctx), tea.WithoutSignalHandler(), tea.WithInput(nil), tea.WithOutput(&output)).Run(); err != nil {
		t.Fatalf("terminal: %v\n%s", err, stripANSI(h.model.View()))
	}
	history := live.History()
	reply := history[len(history)-1]
	if len(reply.Attachments) != 1 || strings.Contains(reply.Content, "![") {
		t.Fatalf("reply: %+v", reply)
	}
	data, err := chatmedia.DefaultStore().Read(reply.Attachments[0])
	if err != nil || !bytes.Equal(data, input) {
		t.Fatalf("stored preview does not match source: %v", err)
	}
	t.Logf("terminal delivered %s (%dx%d), reply %q", reply.Attachments[0].Name, reply.Attachments[0].Width, reply.Attachments[0].Height, reply.Content)
}
