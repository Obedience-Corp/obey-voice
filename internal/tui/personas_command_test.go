package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Obedience-Corp/obey-voice/internal/persona"
	"github.com/Obedience-Corp/obey-voice/internal/talkhotkey"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

func TestPersonasCommandOpensManagerLocally(t *testing.T) {
	for _, input := range []string{"/personas", "/pers"} {
		for _, state := range []turnState{turnIdle, turnVoiceListening, turnVoiceResponding, turnTextRunning} {
			t.Run(fmt.Sprintf("%s/%d", input, state), func(t *testing.T) {
				runner := &fakeTurnRunner{}
				m, _ := startedConversation(t, runner, false)
				m.turnState = state
				m, cmd := typeAndEnter(m, input)
				if cmd == nil {
					t.Fatal("/personas did not request a screen change")
				}
				msg, ok := cmd().(switchScreenMsg)
				if !ok || screen(msg) != screenPersonas {
					t.Fatalf("message = %#v, want screenPersonas", msg)
				}
				if m.input.Value() != "" || len(runner.texts()) != 0 || m.turnState != state {
					t.Fatal("/personas was not consumed locally or changed turn ownership")
				}
			})
		}
	}
}

func TestPersonasReturnsToOriginAndPreservesChat(t *testing.T) {
	config.SetConfigDirForTest(t, t.TempDir())
	for _, origin := range []screen{screenLauncher, screenConversation} {
		for _, voice := range []bool{false, true} {
			for _, key := range []tea.KeyMsg{{Type: tea.KeyEsc}, {Type: tea.KeyRunes, Runes: []rune("q")}} {
				t.Run(fmt.Sprintf("%d/%v/%s", origin, voice, key.String()), func(t *testing.T) {
					cfg := &config.Config{AgentName: "Original"}
					app := App{cfg: cfg, screen: origin, width: 110, height: 35}
					app.conversation = newConversation("Original")
					app.conversation.deps.voice = true
					app.conversation.voiceEnabled = voice
					app.conversation.input.SetValue("Keep this draft")
					app.conversation.seedTranscript([]brain.Turn{{Role: "user", Content: "Keep this history"}})
					model, _ := app.Update(switchScreenMsg(screenPersonas))
					app = model.(App)
					if app.screen != screenPersonas || app.personas.returnScreen != origin {
						t.Fatal("manager did not remember its origin")
					}
					if app.personas.width != 110 || app.personas.height != 35 {
						t.Fatal("manager lost terminal geometry")
					}
					if origin == screenConversation && app.conversation.voiceEnabled {
						t.Fatal("manager left voice input enabled")
					}
					previewClosed, scanCanceled := false, false
					app.personas.preview.previewCancel = func() { previewClosed = true }
					app.personas.brainModels.cancel = func() { scanCanceled = true }
					model, cmd := app.Update(key)
					app = model.(App)
					model, _ = app.Update(cmd())
					app = model.(App)
					if app.screen != origin || app.conversation.voiceEnabled != voice || !previewClosed || !scanCanceled {
						t.Fatal("leaving personas lost origin/mic state or orphaned background work")
					}
					if app.conversation.input.Value() != "Keep this draft" || len(app.conversation.plainTurns) != 1 || app.conversation.agentName != "Original" {
						t.Fatal("leaving personas changed the live chat")
					}
				})
			}
		}
	}
}

func TestPersonasReturnWaitsForVoiceCancellation(t *testing.T) {
	config.SetConfigDirForTest(t, t.TempDir())
	runner := &fakeTurnRunner{}
	conv, _ := startedConversation(t, runner, true)
	app := App{cfg: &config.Config{}, screen: screenConversation, conversation: conv}
	model, _ := app.Update(switchScreenMsg(screenPersonas))
	app = model.(App)
	if app.conversation.turnState != turnVoiceCanceling {
		t.Fatal("opening personas did not cancel listening")
	}
	model, cmd := app.Update(switchScreenMsg(screenConversation))
	app = model.(App)
	if cmd != nil || app.conversation.turnState != turnVoiceCanceling {
		t.Fatal("return dispatched a second voice turn before the first drained")
	}
	model, cmd = app.Update(voiceTurnDoneMsg{})
	app = model.(App)
	if cmd == nil || app.conversation.turnState != turnVoiceListening {
		t.Fatal("voice input did not resume after cancellation drained")
	}
}

func TestPersonasKeepsResponseAndBlocksTalkKeyWhileOpen(t *testing.T) {
	config.SetConfigDirForTest(t, t.TempDir())
	conv, _ := startedConversation(t, &fakeTurnRunner{}, true)
	conv.turnState = turnVoiceResponding
	conv.attachTalk(talkhotkey.Combo{}, talkhotkey.ModeHold)
	app := App{cfg: &config.Config{}, screen: screenConversation, conversation: conv}
	model, _ := app.Update(switchScreenMsg(screenPersonas))
	app = model.(App)
	for _, kind := range []talkhotkey.Kind{talkhotkey.KindDown, talkhotkey.KindUp} {
		model, _ = app.Update(talkHotkeyEventMsg{ev: talkhotkey.Event{Kind: kind}})
		app = model.(App)
		if app.conversation.voiceEnabled || app.conversation.turnState != turnVoiceResponding {
			t.Fatal("talk key started or interrupted a turn behind personas")
		}
	}
	model, _ = app.Update(busEventMsg{event: events.ResponseReady{Response: "Reply while browsing"}})
	app = model.(App)
	model, _ = app.Update(voiceTurnDoneMsg{})
	app = model.(App)
	if app.conversation.lastAssistantText != "Reply while browsing" || app.conversation.turnState != turnIdle {
		t.Fatal("response or completion was lost while personas was open")
	}
}

func TestPersonasSwitchExplainsNewChatBinding(t *testing.T) {
	config.SetConfigDirForTest(t, t.TempDir())
	p := &persona.Profile{Schema: persona.Schema, ID: "research", DisplayName: "Research", Prompts: persona.PromptRefs{Persona: "research"}}
	if err := persona.Write(p, false); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{AgentName: "Original"}
	app := App{cfg: cfg, screen: screenConversation, conversation: newConversation("Original")}
	model, _ := app.Update(switchScreenMsg(screenPersonas))
	app = model.(App)
	for i, item := range app.personas.items {
		if item.ID == p.ID {
			app.personas.cursor = i
		}
	}
	model, _ = app.Update(tea.KeyMsg{Type: tea.KeyEnter})
	app = model.(App)
	if cfg.ActivePersona != p.ID || app.conversation.agentName != "Original" || !strings.Contains(app.personas.message, "start a new chat") {
		t.Fatalf("persona switch changed the live identity or omitted guidance: %s", app.personas.message)
	}
	saved, err := config.Load()
	if err != nil || saved.ActivePersona != p.ID {
		t.Fatalf("persona choice not persisted: config=%+v error=%v", saved, err)
	}
}
