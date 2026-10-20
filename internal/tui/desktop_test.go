package tui

import (
	"strings"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

func TestDesktopSnapshotKeepsReplyIdentityAndCommandOutput(t *testing.T) {
	rt := fakeRuntime()
	a := App{cfg: &config.Config{}, conversation: newConversation("Current persona")}
	a.conversation.rememberUser("Hello")
	a.conversation.appendAgentTurn(chatItem{name: "Original persona", spoken: "Answer", think: "Reasoning"})
	a.conversation.rememberAssistant("Answer")
	a.conversation.commandNotice("First line\nSecond line")
	state := (desktopModel{app: a, rt: rt}).state()
	if len(state.Turns) != 2 || state.Turns[1].Name != "Original persona" || state.Turns[1].Thinking != "Reasoning" {
		t.Fatalf("immutable turn identity lost: %+v", state.Turns)
	}
	if len(state.Notices) != 1 || state.Notices[0] != "First line\nSecond line" {
		t.Fatalf("native command output truncated: %+v", state.Notices)
	}
	a.conversation.clearTranscript()
	if len((desktopModel{app: a, rt: rt}).state().Notices) != 0 {
		t.Fatal("cleared notices remained visible")
	}
}

func TestDesktopManagerPreservesConversationAndMicPreference(t *testing.T) {
	config.SetConfigDirForTest(t, t.TempDir())
	for _, mic := range []bool{false, true} {
		a := App{cfg: &config.Config{}, screen: screenConversation, width: 100, height: 40, conversation: newConversation("Obey")}
		a.conversation.deps.voice = true
		a.conversation.voiceEnabled = mic
		a.conversation.seedTranscript([]brain.Turn{{Role: "user", Content: "Keep this turn"}})
		m := desktopModel{app: a, rt: fakeRuntime()}
		next, _ := m.Update(DesktopAction{Kind: "manager", Text: "workers"})
		m = next.(desktopModel)
		if m.app.conversation.voiceEnabled {
			t.Fatal("microphone stayed on behind manager")
		}
		next, _ = m.Update(DesktopAction{Kind: "return"})
		m = next.(desktopModel)
		if m.app.screen != screenConversation || m.app.conversation.voiceEnabled != mic || len(m.state().Turns) != 1 {
			t.Fatal("return lost chat or microphone preference")
		}
	}
}

func TestDesktopEnrollmentRequiresSettingsAndName(t *testing.T) {
	a := App{cfg: &config.Config{}, screen: screenConversation, conversation: newConversation("Obey")}
	m := desktopModel{app: a, rt: fakeRuntime()}
	next, _ := m.Update(DesktopAction{Kind: "enroll", Text: "Test"})
	m = next.(desktopModel)
	if !strings.Contains(m.state().Status, "Settings") {
		t.Fatal("enrollment was not gated by the visible settings screen")
	}
	m.app.screen = screenSettings
	next, cmd := m.Update(DesktopAction{Kind: "enroll", Text: " "})
	m = next.(desktopModel)
	if cmd != nil || m.app.settings.enroll.active {
		t.Fatal("blank enrollment opened capture")
	}
}
