package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	meetinglog "github.com/Obedience-Corp/obey-voice/internal/meeting/log"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

// plantMeetingBundle writes a listable .meeting bundle holding these events.
func plantMeetingBundle(t *testing.T, dir, id string, lines []string) string {
	t.Helper()
	bundle := filepath.Join(dir, id)
	internal := filepath.Join(bundle, meetinglog.BundleInternalDirName)
	if err := os.MkdirAll(internal, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(internal, meetinglog.BundleEventsName),
		[]byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bundle, meetinglog.BundleDocumentName),
		[]byte("# Meeting: Standup\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return bundle
}

// resumeStartMsg selects the Resume row of the Meeting submenu and returns the
// message it emits, or nil when it emits none.
func resumeStartMsg(t *testing.T, cfg *config.Config) *startMeetingMsg {
	t.Helper()
	m := newLauncher(cfg, nil)
	m = m.openMeetingMenu()
	m.submenuCursor = 1
	if m.submenu[1].action != actionMeetingResume {
		t.Fatalf("row 1 action = %v, want resume", m.submenu[1].action)
	}
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		return nil
	}
	msg, ok := cmd().(startMeetingMsg)
	if !ok {
		t.Fatalf("resume message = %#v", cmd())
	}
	return &msg
}

// The CLI auto-routes a resumed meeting unless it already filed. The launcher
// row used to hardcode keep-local, so an ask/auto user got no picker and no
// delivery after resuming a meeting that had never been routed.
func TestLauncherResumeLeavesRoutingToTheConfiguredMode(t *testing.T) {
	dir := t.TempDir()
	plantMeetingBundle(t, dir, "standup-20260722-090000.meeting", []string{
		`{"type":"session_start","ts":"2026-07-22T09:00:00Z","offset_ms":0,"description":"Standup"}`,
		`{"type":"utterance","ts":"2026-07-22T09:05:00Z","offset_ms":300000,"id":"utterance-1","text":"hello"}`,
		`{"type":"session_end","ts":"2026-07-22T09:30:00Z","offset_ms":1800000}`,
	})

	msg := resumeStartMsg(t, &config.Config{Meeting: config.MeetingConfig{Dir: dir}})
	if msg == nil {
		t.Fatal("resume emitted no start message")
	}
	if msg.RoutePlan != "" {
		t.Fatalf("RoutePlan = %q, want empty so meeting.route.mode decides", msg.RoutePlan)
	}
	if !strings.HasSuffix(msg.ResumePath, "standup-20260722-090000.meeting") {
		t.Fatalf("ResumePath = %q", msg.ResumePath)
	}
}

// A meeting that already filed its notes is the one case both surfaces skip.
func TestLauncherResumeKeepsAlreadyRoutedMeetingLocal(t *testing.T) {
	dir := t.TempDir()
	plantMeetingBundle(t, dir, "standup-20260722-090000.meeting", []string{
		`{"type":"session_start","ts":"2026-07-22T09:00:00Z","offset_ms":0,"description":"Standup"}`,
		`{"type":"route_plan","ts":"2026-07-22T09:00:01Z","offset_ms":1000,"label":"docs","text":"auto"}`,
		`{"type":"session_end","ts":"2026-07-22T09:30:00Z","offset_ms":1800000}`,
		`{"type":"routed","ts":"2026-07-22T09:30:05Z","offset_ms":1805000,"label":"docs","text":"file","message":"delivered: docs"}`,
	})

	msg := resumeStartMsg(t, &config.Config{Meeting: config.MeetingConfig{Dir: dir}})
	if msg == nil {
		t.Fatal("resume emitted no start message")
	}
	if msg.RoutePlan != routePlanLocal {
		t.Fatalf("RoutePlan = %q, want %q for an already-routed meeting", msg.RoutePlan, routePlanLocal)
	}
}
