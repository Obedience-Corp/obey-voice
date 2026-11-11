package tui

import (
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

type testClipboard struct {
	value string
	err   error
}

func (c *testClipboard) ReadAll() (string, error) {
	return c.value, c.err
}

func (c *testClipboard) WriteAll(value string) error {
	c.value = value
	return c.err
}

func TestConversationUsesInjectedClipboardBackend(t *testing.T) {
	m := sizedConversation(t, 80, 24)
	clipboard := &testClipboard{value: "from clipboard"}
	m.deps.clipboard = clipboard
	m.input.SetValue("draft")
	m.moveCursorToOffset(len([]rune("draft")))
	m.syncEditorFromTextarea()

	m, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlV})
	if cmd == nil {
		t.Fatal("Ctrl+V did not request clipboard read")
	}
	m, _ = m.Update(cmd())
	if got := m.input.Value(); got != "draftfrom clipboard" {
		t.Fatalf("clipboard paste = %q, want draftfrom clipboard", got)
	}
}

func TestCopyLastAssistantViaKeysAndCommand(t *testing.T) {
	m := sizedConversation(t, 80, 24)
	clip := &testClipboard{}
	m.deps.clipboard = clip
	m.handleEvent(events.ResponseReady{Response: "Hello from Samantha."})
	if m.lastAssistantText != "Hello from Samantha." {
		t.Fatalf("lastAssistantText = %q", m.lastAssistantText)
	}

	// ctrl+y always yanks the last reply.
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlY})
	if clip.value != "Hello from Samantha." {
		t.Fatalf("ctrl+y clipboard = %q", clip.value)
	}

	// Bare y must type into the composer, even when a reply is ready to yank.
	// Empty-composer yank would steal the first letter of yes/you/yeah.
	clip.value = ""
	m.input.SetValue("")
	m.syncEditorFromTextarea()
	for _, r := range []rune{'y', 'e', 's'} {
		m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	if got := m.input.Value(); got != "yes" {
		t.Fatalf("typing yes from empty composer = %q, want yes", got)
	}
	if clip.value != "" {
		t.Fatalf("bare y must not copy, clipboard=%q", clip.value)
	}

	// /copy all includes user + assistant plain text.
	m.input.SetValue("")
	m.syncEditorFromTextarea()
	m.echoUserTurn("How are you?")
	m.handleEvent(events.ResponseReady{Response: "Doing well."})
	clip.value = ""
	m.runCopyCommand([]string{"all"})
	if !strings.Contains(clip.value, "You: How are you?") || !strings.Contains(clip.value, "Doing well.") {
		t.Fatalf("/copy all clipboard = %q", clip.value)
	}
}

func TestIdleCtrlCCopiesLastReplyInsteadOfQuitting(t *testing.T) {
	app := App{
		cfg:          &config.Config{},
		screen:       screenConversation,
		conversation: sizedConversation(t, 80, 24),
	}
	clip := &testClipboard{}
	app.conversation.deps.clipboard = clip
	app.conversation.lastAssistantText = "Copy me"
	app.conversation.input.SetValue("")
	app.conversation.syncEditorFromTextarea()

	model, cmd := app.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	app = model.(App)
	if app.quitting {
		t.Fatal("idle ctrl+c with a reply must not quit")
	}
	if cmd != nil && cmdEmitsQuit(cmd) {
		t.Fatal("idle ctrl+c returned a quit command")
	}
	if clip.value != "Copy me" {
		t.Fatalf("clipboard = %q, want Copy me", clip.value)
	}
	// Notice should arm second Ctrl+C for quit.
	if app.conversation.lastIdleCopyAt.IsZero() {
		t.Fatal("first idle copy should arm quit window")
	}

	// Second idle Ctrl+C within the window quits.
	app.quitting = false
	model, cmd = app.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	app = model.(App)
	if !app.quitting {
		t.Fatal("second idle ctrl+c must quit")
	}
	if cmd == nil {
		t.Fatal("expected quit command on second ctrl+c")
	}

	// With a draft, ctrl+c still quits.
	app = App{
		cfg:          &config.Config{},
		screen:       screenConversation,
		conversation: sizedConversation(t, 80, 24),
	}
	app.conversation.deps.clipboard = clip
	app.conversation.lastAssistantText = "Copy me"
	app.conversation.input.SetValue("draft")
	app.conversation.syncEditorFromTextarea()
	app.quitting = false
	model, cmd = app.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	app = model.(App)
	if !app.quitting {
		t.Fatal("ctrl+c with a draft must still quit")
	}
	if cmd == nil {
		t.Fatal("expected quit command")
	}
}

func TestCopyAllIncludesSpeakerLabels(t *testing.T) {
	m := sizedConversation(t, 80, 24)
	clip := &testClipboard{}
	m.deps.clipboard = clip
	m.rememberUser("hello from the floor", "speaker-1")
	m.rememberAssistant("heard you")
	m.copyPlainChat()
	if !strings.Contains(clip.value, "speaker-1: hello from the floor") {
		t.Fatalf("/copy all missing speaker label: %q", clip.value)
	}
	if !strings.Contains(clip.value, "heard you") {
		t.Fatalf("/copy all missing assistant text: %q", clip.value)
	}
}

func TestIdleCtrlCQuitsWhenNothingToCopy(t *testing.T) {
	app := App{
		cfg:          &config.Config{},
		screen:       screenConversation,
		conversation: sizedConversation(t, 80, 24),
	}
	clip := &testClipboard{}
	app.conversation.deps.clipboard = clip
	app.conversation.lastAssistantText = ""
	app.conversation.streamingAgent = ""
	app.conversation.input.SetValue("")
	app.conversation.syncEditorFromTextarea()

	model, cmd := app.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	app = model.(App)
	if !app.quitting {
		t.Fatal("idle ctrl+c with nothing to copy must still quit")
	}
	if cmd == nil {
		t.Fatal("expected quit command")
	}
	if clip.value != "" {
		t.Fatalf("clipboard must stay empty, got %q", clip.value)
	}
}

func cmdEmitsQuit(cmd tea.Cmd) bool {
	if cmd == nil {
		return false
	}
	switch msg := cmd().(type) {
	case tea.QuitMsg, quitMsg:
		return true
	case tea.BatchMsg:
		for _, sub := range msg {
			if cmdEmitsQuit(sub) {
				return true
			}
		}
	}
	return false
}

func TestAppCopyKeysUseInjectedClipboard(t *testing.T) {
	clip := &testClipboard{}
	app := newApp(&config.Config{BrainProvider: "ollama"}, nil, nil).withClipboard(clip)
	app.launcher.width, app.launcher.height = 100, 40

	model, _ := app.Update(switchScreenMsg(screenRemote))
	app = model.(App)
	app.remote.url = "https://example.test/join"
	model, cmd := app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("c")})
	app = model.(App)
	if cmd == nil {
		t.Fatal("c did not produce a copy cmd")
	}
	cmd()
	if clip.value != "https://example.test/join" {
		t.Fatalf("remote copy reached %q", clip.value)
	}

	app.screen = screenLauncher
	app.launcher = app.launcher.withBanner("Meeting saved: /tmp/meetings/call.meeting", false)
	model, _ = app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})
	app = model.(App)
	_, cmd = app.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("enter did not produce a copy cmd")
	}
	cmd()
	if clip.value != "/tmp/meetings/call.meeting" {
		t.Fatalf("notice copy reached %q", clip.value)
	}
}

func TestAppRebuiltScreensKeepInjectedClipboard(t *testing.T) {
	clip := &testClipboard{}
	app := newApp(&config.Config{BrainProvider: "ollama"}, nil, nil).withClipboard(clip)
	model, _ := app.Update(switchScreenMsg(screenRemote))
	app = model.(App)
	if app.remote.clipboard != clip {
		t.Fatal("remote screen lost the injected clipboard")
	}
	app.screen = screenPersonas
	model, _ = app.Update(switchScreenMsg(screenLauncher))
	app = model.(App)
	if app.launcher.clipboard != clip {
		t.Fatal("rebuilt launcher lost the injected clipboard")
	}
}

func TestNoticeCopyFailureFlashesCopyFailed(t *testing.T) {
	m := sizedLauncher(t)
	m.clipboard = &testClipboard{err: errors.New("no clipboard")}
	m = m.withBanner("Meeting saved: /tmp/meetings/call.meeting", false)
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})
	m, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("enter did not try to copy")
	}
	m, _ = m.Update(cmd())
	if m.noticeFlash != "copy failed" {
		t.Fatalf("flash = %q, want copy failed", m.noticeFlash)
	}
	if !strings.Contains(stripANSI(m.View()), "copy failed") {
		t.Fatal("view does not show the copy failed flash")
	}
}
