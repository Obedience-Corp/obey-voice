package tui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/capture"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	tea "github.com/charmbracelet/bubbletea"
)

type pendingCaptureStub struct {
	done chan struct{}
	err  error
}

func (s *pendingCaptureStub) Done() <-chan struct{} { return s.done }
func (s *pendingCaptureStub) SealError() error      { return s.err }
func (*pendingCaptureStub) Dir() string             { return "blocked-capture" }
func (*pendingCaptureStub) Mode() string            { return "talk" }

func TestCapturePollForwardedWhileSettingsHidden(t *testing.T) {
	config.SetConfigDirForTest(t, t.TempDir())
	conv, _ := startedConversation(t, &fakeTurnRunner{}, false)
	conv.startCapture("talk")
	recording := conv.capture
	conv.capturePollScheduled = true
	app := newApp(&config.Config{}, nil, nil)
	app.screen = screenSettings
	app.conversation = conv
	model, command := app.Update(capturePollMsg{})
	app = model.(App)
	if command == nil || !app.conversation.capturePollScheduled {
		t.Fatal("hidden active poll lost its timer")
	}
	app.conversation.stopCapture()
	<-recording.Done()
	model, _ = app.Update(capturePollMsg{})
	app = model.(App)
	if app.conversation.capturePending != nil || app.conversation.capturePollScheduled {
		t.Fatal("hidden seal completion was not handled")
	}
}
func TestRunProgramSealsActiveCaptureOnExternalQuitAndError(t *testing.T) {
	for _, programErr := range []error{nil, errors.New("outer terminal failure")} {
		t.Run(fmt.Sprint(programErr), func(t *testing.T) {
			config.SetConfigDirForTest(t, t.TempDir())
			var dir string
			_, err := runProgram(&config.Config{}, nil, nil, false, func(app App) (tea.Model, error) {
				conv, bus := startedConversation(t, &fakeTurnRunner{}, false)
				conv.startCapture("talk")
				dir = conv.capture.Dir()
				bus.Emit(events.Error{Stage: "integration", Message: "persist before outer quit"})
				app.conversation = conv
				// No onQuit call: this is the external Quit/program-error path.
				return app, programErr
			})
			if !errors.Is(err, programErr) {
				t.Fatal(err)
			}
			raw, readErr := os.ReadFile(filepath.Join(dir, "manifest.json"))
			if readErr != nil {
				t.Fatal(readErr)
			}
			var manifest map[string]any
			if readErr = json.Unmarshal(raw, &manifest); readErr != nil {
				t.Fatal(readErr)
			}
			if manifest["status"] != "sealed" {
				t.Fatal("production returned before seal", string(raw))
			}
			eventsRaw, readErr := os.ReadFile(filepath.Join(dir, "events.jsonl"))
			if readErr != nil {
				t.Fatal(readErr)
			}
			if !strings.Contains(string(eventsRaw), "persist before outer quit") || !strings.Contains(string(eventsRaw), `"type":"capture_end"`) {
				t.Fatal("capture footer/content absent")
			}
		})
	}
}
func TestRunProgramBoundsStalledCaptureAndPreservesError(t *testing.T) {
	config.SetConfigDirForTest(t, t.TempDir())
	pending := &pendingCaptureStub{done: make(chan struct{})}
	defer close(pending.done)
	var start time.Time
	_, err := runProgram(&config.Config{}, nil, nil, false, func(app App) (tea.Model, error) {
		app.conversation.capturePending = pending
		start = time.Now()
		return app, errors.New("outer failure")
	})
	if err == nil || !strings.Contains(err.Error(), "outer failure") || !strings.Contains(err.Error(), "capture incomplete/unsealed") {
		t.Fatal("lost capture/program error", err)
	}
	if elapsed := time.Since(start); elapsed > 2900*time.Millisecond {
		t.Fatal("capture wait exceeded shared quit budget", elapsed)
	}
}
func TestMenuPreservesPendingCaptureGuardAndRecovers(t *testing.T) {
	config.SetConfigDirForTest(t, t.TempDir())
	pending := &pendingCaptureStub{done: make(chan struct{})}
	app := newApp(&config.Config{}, nil, nil)
	app.runCtx, app.runCancel = context.WithCancel(context.Background())
	app.slot, app.meetingSlot = newConversationSlot(), newMeetingSlot()
	app.conversation.capturePending = pending
	app.conversation.captureSealStarted = time.Now()
	model, command := app.beginMenuTransition()
	app = model.(App)
	message := command()
	model, _ = app.Update(message)
	app = model.(App)
	if app.conversation.capturePending != pending {
		t.Fatal("menu dropped unresolved capture guard")
	}
	app.conversation.startCapture("talk")
	if app.conversation.capture != nil {
		t.Fatal("replacement opened another writer while old seal blocked")
	}
	close(pending.done)
	model, _ = app.Update(capturePollMsg{})
	app = model.(App)
	if app.conversation.capturePending != nil {
		t.Fatal("completed old capture never released guard")
	}
	app.conversation.startCapture("talk")
	if app.conversation.capture == nil {
		t.Fatal("recording did not recover after old seal completed")
	}
	recording := app.conversation.capture
	app.conversation.stopCapture()
	<-recording.Done()
	app.runCancel()
}

func TestRunProgramDetachesCaptureBeforeBlockedPersist(t *testing.T) {
	config.SetConfigDirForTest(t, t.TempDir())
	entered, release := make(chan struct{}), make(chan struct{})
	result := make(chan error, 1)
	var recording *capture.Session
	var bus *events.Bus
	go func() {
		_, err := runProgram(&config.Config{}, nil, nil, false, func(app App) (tea.Model, error) {
			conv, eventBus := startedConversation(t, &fakeTurnRunner{}, false)
			conv.startCapture("talk")
			recording, bus = conv.capture, eventBus
			app.conversation = conv
			app.slot.store(&ConversationRuntime{Persist: func() { close(entered); <-release }})
			return app, nil
		})
		result <- err
	}()
	<-entered
	bus.Emit(events.Error{Stage: "late", Message: "must not capture after terminal exits"})
	<-recording.Done()
	raw, err := os.ReadFile(recording.EventsPath())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "must not capture") {
		t.Fatal("observer stayed attached while persistence blocked")
	}
	close(release)
	if err := <-result; err != nil {
		t.Fatal(err)
	}
}

func TestCaptureEnvironmentUsesEffectiveSTT(t *testing.T) {
	cases := []struct{ provider, mode, model, wantProvider, wantMode, wantModel string }{
		{"parakeet", "", "tdt-110m", "parakeet", "offline", "tdt-110m"},
		{"  SHERPA-PARAKEET ", "", "tdt-0.6b-v3", "parakeet", "offline", "tdt-0.6b-v3"},
		{"", "", "", "parakeet", "offline", config.DefaultParakeetModel},
		{"sherpa-streaming", "", "", "sherpa", "streaming", "sherpa-streaming-en"},
		{"sherpa", "streaming", "", "sherpa", "streaming", "sherpa-streaming-en"},
		{"", "streaming", "", "sherpa", "streaming", "sherpa-streaming-en"},
	}
	for _, tt := range cases {
		t.Run(tt.provider+"/"+tt.mode, func(t *testing.T) {
			config.SetConfigDirForTest(t, t.TempDir())
			conv, _ := startedConversation(t, &fakeTurnRunner{}, false)
			conv.cfg = &config.Config{STTProvider: tt.provider, STTMode: tt.mode, ParakeetModel: tt.model, WhisperModel: "wrong-whisper"}
			before := *conv.cfg
			conv.startCapture("talk")
			recording := conv.capture
			conv.stopCapture()
			<-recording.Done()
			raw, err := os.ReadFile(recording.EventsPath())
			if err != nil {
				t.Fatal(err)
			}
			var header struct {
				Environment map[string]string `json:"environment"`
			}
			if err := json.Unmarshal([]byte(strings.SplitN(string(raw), "\n", 2)[0]), &header); err != nil {
				t.Fatal(err)
			}
			if header.Environment["stt_provider"] != tt.wantProvider || header.Environment["stt_mode"] != tt.wantMode || header.Environment["stt_model"] != tt.wantModel {
				t.Fatalf("wrong effective STT metadata: %+v", header.Environment)
			}
			if !reflect.DeepEqual(*conv.cfg, before) {
				t.Fatal("recording mutated config")
			}
		})
	}
}
