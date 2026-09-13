package tui

import (
	"errors"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/Obedience-Corp/obey-voice/internal/talkhotkey"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/pipeline"
)

type testWorkers struct {
	items    []pipeline.WorkerInfo
	enabled  bool
	canceled []string
	stopAll  int
	err      error
}

func (f *testWorkers) Workers() []pipeline.WorkerInfo {
	return append([]pipeline.WorkerInfo(nil), f.items...)
}
func (f *testWorkers) SetWorkersEnabled(enabled bool) { f.enabled = enabled }
func (f *testWorkers) CancelWork(id string) error {
	if f.err != nil {
		return f.err
	}
	f.canceled = append(f.canceled, id)
	return nil
}
func (f *testWorkers) StopWorkers() error {
	if f.err != nil {
		return f.err
	}
	f.stopAll++
	return nil
}
func workersKey(m workersModel, key string) workersModel {
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)})
	return m
}

func TestWorkersCommandIsLocalDuringAnyTurn(t *testing.T) {
	for _, state := range []turnState{turnIdle, turnVoiceListening, turnVoiceResponding, turnTextRunning} {
		for _, input := range []string{"/workers", "/work"} {
			runner := &fakeTurnRunner{}
			m, _ := startedConversation(t, runner, false)
			m.turnState = state
			m, cmd := typeAndEnter(m, input)
			if cmd == nil {
				t.Fatal("missing navigation")
			}
			if msg, ok := cmd().(switchScreenMsg); !ok || screen(msg) != screenWorkers {
				t.Fatalf("wrong navigation: %v", msg)
			}
			if len(runner.texts()) != 0 || m.turnState != state || m.input.Value() != "" {
				t.Fatal("command changed turn ownership or reached the brain")
			}
		}
	}
}

func TestWorkersReturnsToSameChatAndMuteState(t *testing.T) {
	config.SetConfigDirForTest(t, t.TempDir())
	for _, origin := range []screen{screenLauncher, screenConversation} {
		for _, voice := range []bool{false, true} {
			app := App{cfg: &config.Config{}, screen: origin, width: 100, height: 28, conversation: newConversation("Original")}
			app.conversation.deps.voice = true
			app.conversation.voiceEnabled = voice
			app.conversation.input.SetValue("draft")
			app.conversation.seedTranscript([]brain.Turn{{Role: "user", Content: "existing history"}})
			model, _ := app.Update(switchScreenMsg(screenWorkers))
			app = model.(App)
			if app.workers.returnScreen != origin || app.workers.width != 100 || app.workers.height != 28 {
				t.Fatal("manager lost navigation or geometry")
			}
			if origin == screenConversation && app.conversation.voiceEnabled {
				t.Fatal("mic remained enabled behind workers")
			}
			model, cmd := app.Update(tea.KeyMsg{Type: tea.KeyEsc})
			app = model.(App)
			model, _ = app.Update(cmd())
			app = model.(App)
			if app.screen != origin || app.conversation.voiceEnabled != voice || app.conversation.input.Value() != "draft" || len(app.conversation.plainTurns) != 1 {
				t.Fatal("return altered the chat or mute state")
			}
		}
	}
}

func TestWorkersKeepsBackgroundCompletionsAndBlocksTalkKey(t *testing.T) {
	config.SetConfigDirForTest(t, t.TempDir())
	conv, _ := startedConversation(t, &fakeTurnRunner{}, true)
	conv.turnState = turnVoiceResponding
	conv.attachTalk(talkhotkey.Combo{}, talkhotkey.ModeHold)
	app := App{cfg: &config.Config{}, screen: screenConversation, conversation: conv}
	model, _ := app.Update(switchScreenMsg(screenWorkers))
	app = model.(App)
	for _, kind := range []talkhotkey.Kind{talkhotkey.KindDown, talkhotkey.KindUp} {
		model, _ = app.Update(talkHotkeyEventMsg{ev: talkhotkey.Event{Kind: kind}})
		app = model.(App)
		if app.conversation.voiceEnabled || app.conversation.turnState != turnVoiceResponding {
			t.Fatal("talk key disturbed the hidden turn")
		}
	}
	model, _ = app.Update(busEventMsg{event: events.ResponseReady{Response: "Reply while browsing"}})
	app = model.(App)
	model, _ = app.Update(voiceTurnDoneMsg{})
	app = model.(App)
	if app.conversation.lastAssistantText != "Reply while browsing" || app.conversation.turnState != turnIdle {
		t.Fatal("lost response/completion behind manager")
	}
}

func TestWorkersPermissionPersistsAndSettingsReenables(t *testing.T) {
	path := realWriterInstall(t, "# preserve me\nvoice_workers_disabled: false\n")
	cfg := &config.Config{}
	controller := &testWorkers{enabled: true, items: []pipeline.WorkerInfo{{ID: "existing", Task: "still running", State: "running"}}}
	m := newWorkers(cfg, controller)
	m = workersKey(m, "t")
	if !strings.Contains(m.message, "Workers off") {
		t.Fatalf("toggle feedback = %q", m.message)
	}
	if !cfg.VoiceWorkersDisabled || controller.enabled || len(controller.canceled) != 0 || controller.stopAll != 0 {
		t.Fatal("toggle didn't disable admission only")
	}
	loaded, err := config.LoadRaw()
	if err != nil || !loaded.VoiceWorkersDisabled {
		t.Fatalf("next chat preference = %+v, %v", loaded, err)
	}
	if !strings.Contains(readWritten(t, path), "# preserve me") {
		t.Fatal("save lost unrelated config")
	}
	settings := newSettings(cfg, nil)
	settings.onWorkersEnabled = controller.SetWorkersEnabled
	for _, row := range settings.workerRows() {
		if row.key == "voice_workers_disabled" {
			row.run(&settings)
		}
	}
	loaded, err = config.LoadRaw()
	if err != nil || loaded.VoiceWorkersDisabled || !controller.enabled || cfg.VoiceWorkersDisabled {
		t.Fatal("Settings did not reenable live and future workers")
	}
}

func TestWorkerPermissionFailedSaveChangesNothing(t *testing.T) {
	for _, settings := range []bool{false, true} {
		cfg := &config.Config{}
		controller := &testWorkers{enabled: true}
		fail := func(string, interface{}) error { return errors.New("disk full") }
		var message string
		if settings {
			m := newSettings(cfg, nil)
			m.saveConfig, m.onWorkersEnabled = fail, controller.SetWorkersEnabled
			for _, row := range m.workerRows() {
				if row.key == "voice_workers_disabled" {
					row.run(&m)
				}
			}
			message = m.message
		} else {
			m := newWorkers(cfg, controller)
			m.saveConfig = fail
			m = workersKey(m, "t")
			message = m.message
		}
		if cfg.VoiceWorkersDisabled || !controller.enabled || !strings.Contains(message, "disk full") {
			t.Fatal("failed save changed runtime or hid error")
		}
	}
}

func TestWorkersDetailsStopControlsAndStableSelection(t *testing.T) {
	controller := &testWorkers{enabled: true, items: []pipeline.WorkerInfo{
		{ID: "a", Task: "inspect source", State: "running", Provider: "ollama", Model: "local", Activity: "read 2 files", Elapsed: 3 * time.Second},
		{ID: "b", Task: "second", State: "queued"},
	}}
	m := newWorkers(&config.Config{}, controller)
	m.width, m.height, m.cursor = 100, 24, 2
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	for _, want := range []string{"inspect source", "ollama / local", "read 2 files", "Output will appear"} {
		if !strings.Contains(stripANSI(m.View()), want) {
			t.Fatalf("detail missing %q", want)
		}
	}
	m = workersKey(m, "s")
	if len(controller.canceled) != 1 || controller.canceled[0] != "a" || !strings.Contains(m.message, "requested") {
		t.Fatal("stop task didn't address original ID")
	}
	m = workersKey(m, "x")
	m = workersKey(m, "n")
	if controller.stopAll != 0 {
		t.Fatal("declining stop all canceled work")
	}
	m = workersKey(m, "x")
	m = workersKey(m, "y")
	if controller.stopAll != 1 {
		t.Fatal("stop all didn't reach runtime")
	}
	controller.items = []pipeline.WorkerInfo{controller.items[1], {ID: "a", Task: "inspect source", State: "aborted", Result: "partial output", Termination: "unknown"}}
	m, _ = m.Update(workersTickMsg{m.token})
	if m.detailID != "a" || m.cursor != 3 {
		t.Fatal("refresh changed selected identity")
	}
	if view := stripANSI(m.View()); !strings.Contains(view, "partial output") || !strings.Contains(view, "Termination: unknown") {
		t.Fatal("terminal output or uncertainty missing")
	}
	m = workersKey(m, "s")
	if len(controller.canceled) != 1 {
		t.Fatal("stopped terminal job")
	}
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if m.detailID != "" {
		t.Fatal("Escape did not return to list")
	}
}

func TestWorkersRenderingAndFooterAreBounded(t *testing.T) {
	for _, size := range [][2]int{{120, 28}, {80, 24}, {40, 10}, {20, 6}} {
		m := newWorkers(&config.Config{VoiceWorkersDisabled: true}, &testWorkers{items: []pipeline.WorkerInfo{{ID: "x", Task: strings.Repeat("界", 100) + "\x1b[31munsafe", State: "running", Activity: strings.Repeat("loaded a skill ", 50)}}})
		m, _ = m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		for _, line := range strings.Split(m.View(), "\n") {
			if lipgloss.Width(line) > size[0] {
				t.Fatalf("line overflow at %v: %q", size, line)
			}
		}
		if len(strings.Split(m.View(), "\n")) > size[1] {
			t.Fatalf("height overflow at %v", size)
		}
		label := workerFooterLabel(workersCountLabel(1, 2, false), size[0])
		if lipgloss.Width(label) > size[0] {
			t.Fatal("worker footer overflow")
		}
		if size[0] >= 40 && (!strings.Contains(label, "/workers") || !strings.Contains(label, "/menu")) {
			t.Fatal("navigation lost in compact footer")
		}
	}
	for _, width := range []int{60, 80, 120} {
		m := sizedConversation(t, width, 24)
		m.handleEvent(events.JobStarted{ID: "x", Task: strings.Repeat("long task ", 100)})
		lines := strings.Split(stripANSI(m.View()), "\n")
		footer := lines[len(lines)-1]
		if strings.Contains(footer, "long task") || !strings.Contains(footer, "/workers") || !strings.Contains(footer, "/menu") {
			t.Fatalf("footer at %d: %s", width, footer)
		}
	}
}

func TestWorkersStaleTickDoesNotStartAnotherRefreshLoop(t *testing.T) {
	old := newWorkers(&config.Config{}, nil)
	next := newWorkers(&config.Config{}, nil)
	_, cmd := next.Update(workersTickMsg{old.token})
	if cmd != nil {
		t.Fatal("stale screen timer was rearmed")
	}
	for _, item := range newLauncher(&config.Config{}, nil).items {
		if item.action == actionWorkers {
			return
		}
	}
	t.Fatal("launcher has no Workers entry")
}

func TestWorkersCancellationErrorsStayVisible(t *testing.T) {
	controller := &testWorkers{items: []pipeline.WorkerInfo{{ID: "busy", Task: "work", State: "running"}}, err: errors.New("backend unavailable")}
	m := newWorkers(&config.Config{}, controller)
	m.cursor = 2
	m = workersKey(m, "s")
	if !strings.Contains(m.message, "Stop failed: backend unavailable") {
		t.Fatalf("single stop error = %q", m.message)
	}
	m = workersKey(m, "x")
	m = workersKey(m, "y")
	if !strings.Contains(m.message, "Stop failed: backend unavailable") || controller.stopAll != 0 || len(controller.canceled) != 0 {
		t.Fatal("failed cancellation was reported as success")
	}
}
