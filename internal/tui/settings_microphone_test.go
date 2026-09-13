package tui

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

func microphoneSettingsApp(t *testing.T, change func(context.Context, string) error) App {
	t.Helper()
	app := App{cfg: &config.Config{InputDevice: "Wireless Mic"}, screen: screenConversation, runCtx: context.Background()}
	app.runtime = &ConversationRuntime{Voice: true, InputDevice: "Wireless Mic", SetInputDevice: change,
		SessionCfg: &config.Config{InputDevice: "Wireless Mic"}}
	app.conversation = newConversation("Samantha")
	app.conversation.deps.voice = true
	app.conversation.voiceEnabled = true
	app.conversation.input.SetValue("preserve this draft")
	model, _ := app.Update(switchScreenMsg(screenSettings))
	app = model.(App)
	app.settings.devicesLoading = false
	app.settings.saveConfig = func(string, any) error { return nil }
	return app
}

func TestSettingsRebindsMicrophoneBeforeResumingConversation(t *testing.T) {
	for _, name := range []string{"Studio Display Microphone", ""} {
		t.Run(deviceLabel(name), func(t *testing.T) {
			var opened []string
			app := microphoneSettingsApp(t, func(_ context.Context, name string) error {
				opened = append(opened, name)
				return nil
			})
			var saved string
			app.settings.saveConfig = func(key string, value any) error {
				if key != "input_device" {
					t.Fatalf("saved key = %q", key)
				}
				saved = value.(string)
				return nil
			}
			app.settings.selectInputDevice(name)
			model, cmd := app.Update(settingsDoneMsg{})
			app = model.(App)
			if cmd == nil || app.conversation.voiceEnabled {
				t.Fatal("must rebind before resuming input")
			}
			model, _ = app.Update(cmd())
			app = model.(App)
			if len(opened) != 1 || opened[0] != name || saved != name {
				t.Fatalf("opened=%v saved=%q", opened, saved)
			}
			if !app.conversation.voiceEnabled || app.runtime.InputDevice != name || app.runtime.SessionCfg.InputDevice != name {
				t.Fatal("successful rebind did not publish selection and resume voice")
			}
			if app.conversation.input.Value() != "preserve this draft" {
				t.Fatal("conversation draft lost")
			}
			if !strings.Contains(app.conversation.status, deviceLabel(name)) {
				t.Fatalf("status=%q", app.conversation.status)
			}
		})
	}
}

func TestSettingsFailedMicrophoneSwitchKeepsInputPausedAndOldDevice(t *testing.T) {
	app := microphoneSettingsApp(t, func(context.Context, string) error { return errors.New("Studio Display unavailable") })
	app.settings.selectInputDevice("Studio Display Microphone")
	model, cmd := app.Update(settingsDoneMsg{})
	app = model.(App)
	model, _ = app.Update(cmd())
	app = model.(App)
	if app.conversation.voiceEnabled || app.runtime.InputDevice != "Wireless Mic" {
		t.Fatal("failed switch resumed capture or claimed new device")
	}
	if !strings.Contains(app.conversation.status, "Studio Display unavailable") {
		t.Fatalf("status=%q", app.conversation.status)
	}
	if app.cfg.InputDevice != "Studio Display Microphone" {
		t.Fatal("saved preference lost on runtime failure")
	}
}

func TestSettingsUnchangedOrUnsavedMicrophoneDoesNotRebind(t *testing.T) {
	for _, failSave := range []bool{false, true} {
		app := microphoneSettingsApp(t, func(context.Context, string) error { t.Fatal("unexpected rebind"); return nil })
		if failSave {
			app.settings.saveConfig = func(string, any) error { return errors.New("read only") }
			app.settings.selectInputDevice("Studio Display Microphone")
		}
		model, _ := app.Update(settingsDoneMsg{})
		if !model.(App).conversation.voiceEnabled {
			t.Fatal("unchanged capture did not resume")
		}
	}
}

func TestSettingsMicrophoneThenVoiceReloadKeepsInputPausedUntilBothFinish(t *testing.T) {
	var order []string
	app := microphoneSettingsApp(t, func(context.Context, string) error { order = append(order, "mic"); return nil })
	app.runtime.ReloadVoice = func(context.Context) error { order = append(order, "voice"); return nil }
	app.settings.selectInputDevice("Studio Display Microphone")
	app.cfg.TTSVoice = "new-voice"
	model, cmd := app.Update(settingsDoneMsg{})
	app = model.(App)
	model, cmd = app.Update(cmd())
	app = model.(App)
	if app.conversation.voiceEnabled || cmd == nil {
		t.Fatal("voice resumed before TTS reload")
	}
	model, _ = app.Update(cmd())
	app = model.(App)
	if strings.Join(order, ",") != "mic,voice" || !app.conversation.voiceEnabled {
		t.Fatalf("order=%v", order)
	}
}

func TestMicrophoneResultCannotRelabelAnotherRuntime(t *testing.T) {
	app := microphoneSettingsApp(t, nil)
	previous := app.runtime
	app.runtime = &ConversationRuntime{InputDevice: "new session mic"}
	model, _ := app.Update(microphoneSwitchedMsg{runtime: previous, name: "old session mic", resumeVoice: true})
	if model.(App).runtime.InputDevice != "new session mic" || model.(App).conversation.voiceEnabled {
		t.Fatal("stale rebind result affected a different conversation")
	}
}
