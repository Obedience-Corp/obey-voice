package tui

import (
	"context"
	"errors"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/stt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

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

// Exercise the running Bubble Tea driver, including cancellation before and
// immediately after startup, without opening a microphone or helper process.
func TestDesktopControllerCancellationPersistsAndCleansUp(t *testing.T) {
	config.SetConfigDirForTest(t, t.TempDir())
	for _, beforeStart := range []bool{true, false} {
		for i := 0; i < 10; i++ {
			ctx, cancel := context.WithCancel(context.Background())
			rt := fakeRuntime()
			rt.SessionCfg = &config.Config{}
			rt.Voice = true
			var order []string
			rt.Persist = func() { order = append(order, "persist") }
			rt.Cleanup = func() { order = append(order, "cleanup") }
			if beforeStart {
				cancel()
			}
			controller := StartDesktopController(ctx, rt, nil)
			cancel()
			done := make(chan error, 1)
			go func() { done <- controller.Wait() }()
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(time.Second):
				t.Fatal("startup cancellation stranded controller")
			}
			if strings.Join(order, ",") != "persist,cleanup" {
				t.Fatalf("shutdown order: %v", order)
			}
		}
	}
}

func TestDesktopSettingsRoundTripDoesNotReloadSessionVoice(t *testing.T) {
	config.SetConfigDirForTest(t, t.TempDir())
	if err := config.SetAndSave("tts_provider", "kokoro"); err != nil {
		t.Fatal(err)
	}
	rt := fakeRuntime()
	rt.SessionCfg = &config.Config{TTSProvider: "qwen3-tts", QwenTTSVoice: "Uncle_Fu"}
	rt.Voice = true
	rt.Pipeline.STT = desktopQuietSTT{}
	rt.Pipeline.Events = rt.Bus
	var reloads atomic.Int32
	rt.ReloadVoice = func(context.Context) error { reloads.Add(1); return nil }
	rt.Persist, rt.Cleanup = func() {}, func() {}
	states := make(chan DesktopState, 32)
	ctx, cancel := context.WithCancel(context.Background())
	controller := StartDesktopController(ctx, rt, func(s DesktopState) {
		select {
		case states <- s:
		default:
		}
	})
	defer func() { cancel(); _ = controller.Wait() }()
	controller.Send(DesktopAction{Kind: "manager", Text: "settings"})
	controller.Send(DesktopAction{Kind: "return"})
	// A slash-command notice proves both preceding actions reached Update.
	controller.Send(DesktopAction{Kind: "text", Text: "/help"})
	deadline := time.After(2 * time.Second)
	for {
		select {
		case s := <-states:
			if len(s.Notices) > 0 {
				if !s.Mic {
					t.Fatal("no-op settings visit did not resume microphone admission")
				}
				if reloads.Load() != 0 {
					t.Fatal("no-op settings visit reloaded the session voice")
				}
				return
			}
		case err := <-controller.done:
			t.Fatalf("controller exited early: %v", err)
		case <-deadline:
			t.Fatal("controller did not finish settings round trip")
		}
	}
}

func TestDesktopPreviewUsesSettingsSnapshot(t *testing.T) {
	config.SetConfigDirForTest(t, t.TempDir())
	if err := config.SetAndSave("tts_provider", "kokoro"); err != nil {
		t.Fatal(err)
	}
	player := newFakePreviewPlayer()
	settings := newPreviewTestModel(player, nil)
	settings.cfg = &config.Config{TTSProvider: "qwen3-tts", QwenTTSVoice: "Uncle_Fu", OutputDevice: "Session speaker", SpeechSpeed: 1.2}
	var previewCfg config.Config
	settings.ensureTTSAssets = func(_ context.Context, cfg *config.Config, _ func(string, float64)) error {
		previewCfg = *cfg
		return errors.New("fixture stops before hardware")
	}
	m := desktopModel{app: App{cfg: settings.cfg, screen: screenSettings, settings: settings}, rt: fakeRuntime()}
	next, cmd := m.Update(DesktopAction{Kind: "preview", Text: "Uncle_Fu"})
	if cmd == nil {
		t.Fatal("preview command missing")
	}
	cmd()
	updated := next.(desktopModel)
	defer updated.app.settings.closePreview()
	if previewCfg.TTSProvider != "qwen3-tts" || previewCfg.OutputDevice != "Session speaker" || previewCfg.SpeechSpeed != 1.2 {
		t.Fatalf("preview replaced settings scope: %+v", previewCfg)
	}
}

// Capture stub waits for cancellation; it never opens an audio device.
type desktopQuietSTT struct{}

func (desktopQuietSTT) Available() bool { return true }
func (desktopQuietSTT) Start(context.Context) (stt.Session, error) {
	return &desktopQuietSession{events: make(chan stt.Event)}, nil
}

type desktopQuietSession struct {
	events chan stt.Event
	once   sync.Once
}

func (s *desktopQuietSession) Events() <-chan stt.Event { return s.events }
func (s *desktopQuietSession) Close() error             { s.once.Do(func() { close(s.events) }); return nil }

func TestDesktopSavedSettingsRefreshKeepsEntryFingerprint(t *testing.T) {
	config.SetConfigDirForTest(t, t.TempDir())
	if err := config.SetAndSave("tts_provider", "kokoro"); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadRaw()
	if err != nil {
		t.Fatal(err)
	}
	rt := fakeRuntime()
	rt.ReloadVoice = func(context.Context) error { return nil }
	a := newApp(cfg, nil, nil)
	a.screen, a.runtime = screenConversation, rt
	a.runCtx = context.Background()
	m := desktopModel{app: a, rt: rt}
	next, _ := m.Update(DesktopAction{Kind: "manager", Text: "settings"})
	m = next.(desktopModel)
	baseline := m.app.settingsVoiceState
	if err := config.SetAndSave("tts_voice", "af_bella"); err != nil {
		t.Fatal(err)
	}
	next, _ = m.Update(DesktopAction{Kind: "settings_refresh"})
	m = next.(desktopModel)
	if m.app.settings.cfg.TTSVoice != "af_bella" || m.app.settingsVoiceState != baseline {
		t.Fatal("saved settings did not refresh preview snapshot independently of baseline")
	}
	next, cmd := m.Update(DesktopAction{Kind: "return"})
	m = next.(desktopModel)
	if cmd == nil || !strings.Contains(m.app.conversation.status, "Applying voice") {
		t.Fatal("actual voice edit did not apply on return")
	}
	if _, ok := cmd().(voiceReloadedMsg); !ok {
		t.Fatal("return did not execute voice reload")
	}
}
