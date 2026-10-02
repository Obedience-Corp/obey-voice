package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

const routeJSON = `Meeting notes routed to camp:JobSearch: {"schema_version":"intent-meeting-import/v1alpha1","campaign_root":"/tmp/JobSearch","generated_at":"2026-10-02T18:17:48Z","note":{"duration_seconds":44270,"path":"meetings/test-20261002-121748.md","transcript":"transcripts/test-20261002-121748.txt","speakers":1,"utterances":1,"updated_existing":false}}`

func TestNoticeLogOpensFilingDetail(t *testing.T) {
	m := sizedLauncher(t)
	m = m.withBanner("Meeting saved: /tmp/meetings/test-20261002-121718.meeting", false)
	m = m.withBanner(routeJSON, false)
	if len(m.notices) != 2 {
		t.Fatalf("log length = %d, want 2", len(m.notices))
	}
	if m.notices[0].line != "Meeting notes filed to camp:JobSearch" {
		t.Fatalf("newest = %q", m.notices[0].line)
	}
	if !strings.HasSuffix(m.notices[1].line, "test-20261002-121718.meeting") {
		t.Fatalf("older notice dropped: %#v", m.notices)
	}

	plain := stripANSI(m.View())
	if strings.Contains(plain, "campaign_root") || strings.Contains(plain, "schema_version") {
		t.Fatalf("collapsed view leaked the receipt:\n%s", plain)
	}
	if !strings.Contains(plain, " n ") || !strings.Contains(plain, "  2") {
		t.Fatalf("rail missing key or count:\n%s", plain)
	}

	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})
	if !m.noticesOpen {
		t.Fatal("n did not open notices")
	}
	open := stripANSI(m.View())
	for _, want := range []string{"Notices", "meetings/test-20261002-121748.md", "recorded 12h 17m", "new file", "enter copy note"} {
		if !strings.Contains(open, want) {
			t.Fatalf("open view missing %q:\n%s", want, open)
		}
	}
	if strings.Contains(open, "schema_version") || strings.Contains(open, "generated_at") || strings.Contains(open, "New conversation") {
		t.Fatalf("open view still shows the menu or the raw receipt:\n%s", open)
	}
	if m.notices[0].copyLabel != "note" || !strings.HasSuffix(m.notices[0].copyText, "test-20261002-121748.md") {
		t.Fatalf("copy target = %q %q", m.notices[0].copyLabel, m.notices[0].copyText)
	}
}

func TestNoticeEnterNamesTheCopyTarget(t *testing.T) {
	m := sizedLauncher(t)
	m = m.withBanner("Meeting saved: /tmp/meetings/call.meeting", false)
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})
	if m.notices[0].copyLabel != "path" || m.notices[0].copyText != "/tmp/meetings/call.meeting" {
		t.Fatalf("saved copy = %q %q", m.notices[0].copyLabel, m.notices[0].copyText)
	}
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("enter did not try to copy")
	}
	if _, ok := cmd().(noticeCopiedMsg); !ok {
		t.Fatalf("copy message = %#v", cmd())
	}
	if !next.noticesOpen {
		t.Fatal("enter closed the log")
	}

	ended := "Meeting ended with error: stt failed — NOT filed to camp:JobSearch; recover with: obey-voice meeting route /tmp/x.meeting --to camp:JobSearch"
	m = sizedLauncher(t).withBanner(ended, true)
	if m.notices[0].line != "Meeting ended · not filed to camp:JobSearch" {
		t.Fatalf("ended line = %q", m.notices[0].line)
	}
	if m.notices[0].copyLabel != "command" || !strings.HasPrefix(m.notices[0].copyText, "obey-voice meeting route ") {
		t.Fatalf("recover copy = %q %q", m.notices[0].copyLabel, m.notices[0].copyText)
	}
	var sawCause bool
	for _, d := range m.notices[0].detail {
		if strings.Contains(d.text, "stt failed") {
			sawCause = true
		}
	}
	if !sawCause {
		t.Fatalf("ended detail = %#v", m.notices[0].detail)
	}
}

func TestNoticeProgressStaysOffTheLog(t *testing.T) {
	m := sizedLauncher(t)
	m = m.withBanner("Discovering destinations…", false)
	if len(m.notices) != 0 || m.progressLine == "" {
		t.Fatalf("progress stored: log=%d line=%q", len(m.notices), m.progressLine)
	}
	view := stripANSI(m.View())
	if !strings.Contains(view, "Discovering destinations…") || strings.Contains(view, " n ") {
		t.Fatalf("empty-log progress rail:\n%s", view)
	}
	m, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})
	if m.noticesOpen || cmd != nil {
		t.Fatal("n opened notices with an empty log")
	}

	m = m.withBanner("Meeting saved: /tmp/a.meeting", false)
	m = m.withBanner("Filing meeting notes…", false)
	if len(m.notices) != 1 || strings.HasPrefix(m.notices[0].line, "Filing") {
		t.Fatalf("progress replaced the log: %#v", m.notices)
	}
	again := stripANSI(m.View())
	if !strings.Contains(again, "Filing meeting notes…") || !strings.Contains(again, " n ") {
		t.Fatalf("progress hid the older notice:\n%s", again)
	}
}

func TestNoticeQReturnsToTheMenu(t *testing.T) {
	m := sizedLauncher(t).withBanner("Meeting route failed (notes kept local): disk full", true)
	if m.notices[0].line != "Meeting notes not filed" || !m.notices[0].err {
		t.Fatalf("failure = %#v", m.notices[0])
	}
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})
	open := stripANSI(m.View())
	if !strings.Contains(open, "disk full") || strings.Contains(open, "New conversation") {
		t.Fatalf("failure detail:\n%s", open)
	}
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	if next.noticesOpen {
		t.Fatal("q left notices open")
	}
	if cmd != nil {
		if _, quit := cmd().(quitMsg); quit {
			t.Fatal("q from notices quit")
		}
	}
	if !strings.Contains(stripANSI(next.View()), "New conversation") {
		t.Fatal("closing notices did not restore the menu")
	}
}

func TestNoticeNIgnoresAnOpenSubmenu(t *testing.T) {
	m := sizedLauncher(t).withBanner("Meeting saved: /tmp/a.meeting", false)
	m = m.openMeetingMenu()
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})
	if next.noticesOpen || next.submenu == nil {
		t.Fatalf("n changed the meeting menu: open=%v submenu=%v", next.noticesOpen, next.submenu != nil)
	}
}

func TestNoticeSelectionSurvivesANewArrival(t *testing.T) {
	m := sizedLauncher(t)
	m = m.withBanner("Meeting saved: /tmp/first.meeting", false)
	m = m.withBanner("Speaker analysis failed: boom", true)
	m.noticesOpen = true
	m.noticeCursor = 1
	reading := m.notices[1].id
	m = m.withBanner("Meeting notes kept local only (no destinations available)", false)
	if m.notices[m.noticeCursor].id != reading {
		t.Fatalf("cursor jumped to id %d, want %d", m.notices[m.noticeCursor].id, reading)
	}
	if m.notices[0].line != "Meeting notes kept on this Mac" {
		t.Fatalf("newest = %q", m.notices[0].line)
	}
}

func TestNoticeLogCapsAtTwenty(t *testing.T) {
	m := sizedLauncher(t)
	for i := range 25 {
		m = m.withBanner(fmt.Sprintf("Meeting saved: /tmp/%d.meeting", i), false)
	}
	if len(m.notices) != noticeLogCap {
		t.Fatalf("len = %d", len(m.notices))
	}
	if m.notices[0].copyText != "/tmp/24.meeting" {
		t.Fatalf("newest = %q", m.notices[0].copyText)
	}
	if got := m.notices[noticeLogCap-1].copyText; got != "/tmp/5.meeting" {
		t.Fatalf("oldest = %q, want /tmp/5.meeting", got)
	}
}

func TestNoticeBrowserFitsShortTerminal(t *testing.T) {
	m := newLauncher(&config.Config{BrainProvider: "ollama"}, nil)
	m.width, m.height = 60, 8
	for i := range 12 {
		m = m.withBanner(fmt.Sprintf("Meeting saved: /tmp/meetings/note-%02d.meeting", i), false)
	}
	m = m.withBanner(routeJSON, false)
	m.noticesOpen = true
	m.noticeCursor = 0

	view := stripANSI(m.View())
	if got := len(strings.Split(view, "\n")); got > m.height {
		t.Fatalf("short launcher rendered %d lines:\n%s", got, view)
	}
	for _, want := range []string{"Meeting notes filed to camp:JobSearch", "test-20261002-121", "enter copy note", "esc/q back"} {
		if !strings.Contains(view, want) {
			t.Fatalf("short view missing %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "note-00.meeting") {
		t.Fatalf("off-window row still rendered:\n%s", view)
	}

	m.noticeCursor = len(m.notices) - 1
	low := stripANSI(m.View())
	if got := len(strings.Split(low, "\n")); got > m.height {
		t.Fatalf("scrolled launcher rendered %d lines:\n%s", got, low)
	}
	if !strings.Contains(low, "note-00.meeting") || !strings.Contains(low, "esc/q back") || !strings.Contains(low, "12–13 / 13") {
		t.Fatalf("scrolled selection left the screen:\n%s", low)
	}
	if strings.Contains(low, "note-11.meeting") {
		t.Fatalf("window did not follow the cursor:\n%s", low)
	}
}

func TestParseNoticeSentences(t *testing.T) {
	recovered := parseNotice("Recovered pending meeting route — Meeting notes routed to camp:JobSearch: /tmp/note.md", false, time.Time{})
	if recovered.line != "Recovered a meeting" || recovered.copyLabel != "path" || recovered.copyText != "/tmp/note.md" {
		t.Fatalf("recovered one = %#v", recovered)
	}
	many := parseNotice("Recovered 3 pending meeting routes", false, time.Time{})
	if many.line != "Recovered 3 meetings" || many.copyText != "" {
		t.Fatalf("recovered many = %#v", many)
	}
	retry := parseNotice("Meeting route retry: 1 delivered, 1 failed (boom)", true, time.Time{})
	if retry.line != "Meeting recovery incomplete" || retry.copyText != "" {
		t.Fatalf("retry = %#v", retry)
	}
	speakers := parseNotice("Speaker labels updated: 7 speakers · /tmp/speaker-analysis.json", false, time.Time{})
	if speakers.line != "Speakers labeled · 7" || speakers.copyLabel != "path" {
		t.Fatalf("speakers = %#v", speakers)
	}
}

func sizedLauncher(t *testing.T) launcherModel {
	t.Helper()
	m := newLauncher(&config.Config{BrainProvider: "ollama"}, nil)
	m.width, m.height = 100, 40
	return m
}
