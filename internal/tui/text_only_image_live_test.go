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

// An installed model that cannot view images answers in a conversation where
// another persona was shown a picture.
func TestLiveTextOnlyModelAnswersAfterAPicture(t *testing.T) {
	model := os.Getenv("OBEY_VOICE_TEXT_ONLY_MODEL")
	if model == "" {
		t.Skip("set OBEY_VOICE_TEXT_ONLY_MODEL to an installed local model without vision")
	}
	root := t.TempDir()
	config.SetConfigDirForTest(t, filepath.Join(root, "config"))
	ref, err := chatmedia.DefaultStore().Import("screen.png", testmedia.PNG())
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{OllamaHost: "http://localhost:11434", OllamaModel: model, OllamaNumCtx: 32768, WorkDir: root, WorkerBoundary: root, PromptsDir: filepath.Join(root, "prompts"), SkillsDir: filepath.Join(root, "skills"), AgentName: "Venessa", MaxHistory: 20, ContinuityIntegrityEnabled: true}
	live, err := brain.NewOllama(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := live.ImportHistory([]brain.Turn{
		{Role: "user", Content: "What is on my screen?", Attachments: []chatmedia.Ref{ref}},
		{Role: "assistant", Content: "A terminal with a list of open tasks and a note about duplicate app launches.", PersonaID: "obey", PersonaDisplayName: "Obey"},
	}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	bus := events.NewBus()
	p := &pipeline.Pipeline{Brain: live, Events: bus}
	m := sizedConversation(t, 100, 42)
	m.cfg = cfg
	init := m.startConversation(conversationDeps{runner: p, ctx: ctx, bus: bus})
	m.input.SetValue("Venessa, what do you think?")
	h := &imageRepairTerminal{model: m, initial: init, wantImages: -1, stopOnTurnEnd: true}
	var output bytes.Buffer
	if _, err := tea.NewProgram(h, tea.WithContext(ctx), tea.WithoutSignalHandler(), tea.WithInput(nil), tea.WithOutput(&output)).Run(); err != nil {
		t.Fatalf("terminal: %v\n%s", err, stripANSI(h.model.View()))
	}
	view := stripANSI(h.model.View())
	if strings.Contains(view, "Error:") || !strings.Contains(view, "was not shown screen.png") {
		t.Fatalf("terminal shows:\n%s", view)
	}
	history := live.History()
	reply := history[len(history)-1]
	if reply.Role != "assistant" || strings.TrimSpace(reply.Content) == "" || len(history[0].Attachments) != 1 {
		t.Fatalf("history: %+v", history)
	}
	t.Logf("reply %q", reply.Content)
}
