package tui

import (
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/capture"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

func TestRecordTalkSealsAdmitError(t *testing.T) {
	config.SetConfigDirForTest(t, t.TempDir())
	m, bus := startedConversation(t, &fakeTurnRunner{}, false)
	m.sessionID = "sess-record"
	m.deps.sessionID = "sess-record"

	m.configureRecord([]string{"talk"})
	if m.recordMode() != "talk" {
		t.Fatalf("mode = %q", m.recordMode())
	}
	view := stripANSI(m.View())
	if !strings.Contains(view, "rec") {
		t.Fatalf("missing rec chip:\n%s", view)
	}

	bus.Emit(events.Error{Stage: "admit", Message: "admit response: persona name \"veronica\" is ambiguous between veronica, veronica-hype"})
	recording := m.capture
	dir := recording.Dir()
	m.configureRecord([]string{"off"})
	if m.recordMode() != "" {
		t.Fatal("still recording after off")
	}

	<-recording.Done()
	raw, err := os.ReadFile(filepath.Join(dir, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "ambiguous") {
		t.Fatalf("events.jsonl missing admit error:\n%s", raw)
	}
	if _, err := os.Stat(filepath.Join(dir, "manifest.json")); err != nil {
		t.Fatal(err)
	}
}

func TestCtrlRTogglesTalkNotInVim(t *testing.T) {
	config.SetConfigDirForTest(t, t.TempDir())
	m, _ := startedConversation(t, &fakeTurnRunner{}, false)
	m.sessionID = "sess-r"
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlR})
	if m.recordMode() != "talk" {
		t.Fatalf("ctrl+r mode = %q want talk", m.recordMode())
	}
	recording := m.capture
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlR})
	<-recording.Done()
	if m.recordMode() != "" {
		t.Fatalf("second ctrl+r mode = %q want off", m.recordMode())
	}
}

func TestRecordStopsOnClearAndNewRuntime(t *testing.T) {
	for _, reset := range []string{"clear", "new runtime"} {
		t.Run(reset, func(t *testing.T) {
			config.SetConfigDirForTest(t, t.TempDir())
			m, bus := startedConversation(t, &fakeTurnRunner{}, false)
			m.startCapture("talk")
			recording := m.capture
			if reset == "clear" {
				m.handleEvent(events.ConversationCleared{})
			} else {
				m.startConversation(m.deps)
			}
			if m.capture != nil || m.capturePending == nil {
				t.Fatal("capture survived conversation identity reset")
			}
			bus.Emit(events.UserInput{Text: "late after reset"})
			<-recording.Done()
			raw, err := os.ReadFile(filepath.Join(recording.Dir(), "events.jsonl"))
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(raw), "late after reset") {
				t.Fatal("late event recorded after detach")
			}
			m.pollCapture()
			if m.capturePending != nil {
				t.Fatal("completed seal retained pending writer")
			}
		})
	}
}

func TestRecordingNoticesLinkToBundleWithoutShowingItsPath(t *testing.T) {
	root := filepath.Join(t.TempDir(), "Library #1 ? café")
	config.SetConfigDirForTest(t, root)
	m, _ := startedConversation(t, &fakeTurnRunner{}, false)
	m.startCapture("talk")
	recording := m.capture
	t.Cleanup(func() { m.stopCapture(); <-recording.Done() })
	target := (&url.URL{Scheme: "file", Path: filepath.ToSlash(recording.Dir())}).String()
	check := func(stage string) {
		t.Helper()
		view := m.View()
		if !strings.Contains(view, ansi.SetHyperlink(target)) || !strings.Contains(view, ansi.ResetHyperlink()) {
			t.Fatalf("%s: rendered link missing its encoded target", stage)
		}
		item := m.items[len(m.items)-1]
		plain := ansi.Strip(item.sys)
		if !strings.Contains(plain, "Open recording") || strings.Contains(plain, root) || strings.Contains(plain, "file:") || strings.Contains(plain, "events.jsonl") {
			t.Fatalf("%s: recording notice = %q", stage, plain)
		}
		if strings.Contains(item.notice, "\x1b") || strings.Contains(m.activity[len(m.activity)-1].detail, "\x1b") {
			t.Fatalf("%s: terminal controls leaked into native notice or activity data", stage)
		}
	}
	check("start")
	m.configureRecord(nil)
	check("status")
	m.startCapture("talk")
	check("already recording")
	m.stopCapture()
	check("sealing")
	<-recording.Done()
	m.pollCapture()
	check("sealed")
	if _, err := os.Stat(filepath.Join(recording.Dir(), "manifest.json")); err != nil {
		t.Fatalf("linked recording bundle unavailable: %v", err)
	}
}

type recordingRunner struct {
	*fakeTurnRunner
	sources audio.RecordingSources
}

func (r *recordingRunner) RecordingSources() audio.RecordingSources { return r.sources }

func TestRecordingFollowsMicrophonePause(t *testing.T) {
	config.SetConfigDirForTest(t, t.TempDir())
	m, _ := startedConversation(t, &fakeTurnRunner{}, true)
	var mic audio.RecordingFeed
	m.deps.runner = &recordingRunner{fakeTurnRunner: &fakeTurnRunner{}, sources: audio.RecordingSources{User: &mic}}
	m.startCapture("talk")
	recording := m.capture
	m.setInputMuted(true)
	frame := audio.Frame{Samples: make([]float32, 1600), SampleRate: audio.SampleRate, Channels: 1, StartedAt: time.Now()}
	mic.Publish(frame)
	m.setInputMuted(false)
	mic.Publish(frame)
	m.stopCapture()
	<-recording.Done()
	if err := recording.SealError(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(recording.Dir(), "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest capture.Manifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Audio.User.Frames != 1 {
		t.Fatalf("paused microphone was recorded: %+v", manifest.Audio.User)
	}
}
