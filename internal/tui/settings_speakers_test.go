package tui

import (
	"strings"
	"testing"

	"github.com/Obedience-Corp/obey-voice/internal/speaker"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

// newSpeakerSettings builds a Speakers-section model with a recording saveConfig.
func newSpeakerSettings(t *testing.T, cfg *config.Config, row int) (*settingsModel, map[string]any) {
	t.Helper()
	m := newSettings(cfg, nil)
	m.section = sectionSpeakers
	m.cursor = row
	saved := map[string]any{}
	m.saveConfig = func(key string, value any) error {
		saved[key] = value
		return nil
	}
	return &m, saved
}

// TestSettingsSpeakersToggleEnablesLive is the gap this section closes: chat
// speaker labels were reachable only by hand-editing config.yaml.
func TestSettingsSpeakersToggleEnablesLive(t *testing.T) {
	cfg := &config.Config{}
	m, saved := newSpeakerSettings(t, cfg, speakerRowLive)

	m.selectCurrent()

	if saved["speaker.enabled"] != true {
		t.Errorf("master speaker.enabled saved = %v, want true", saved["speaker.enabled"])
	}
	if saved["speaker.live.enabled"] != true {
		t.Errorf("speaker.live.enabled saved = %v, want true", saved["speaker.live.enabled"])
	}
	if !cfg.Speaker.Enabled || !cfg.Speaker.Live.Enabled {
		t.Errorf("cfg speaker = %+v, want master and live on", cfg.Speaker)
	}
	// The running conversation needs /speakers on; only new chats pick it up
	// from config, and the message must not imply otherwise.
	if !strings.Contains(m.message, "/speakers on") {
		t.Errorf("message = %q, want the /speakers on hint", m.message)
	}
}

// TestSettingsSpeakersDisableLiveKeepsMeeting guards the asymmetry: live off
// must not switch off the master flag that meeting diarization also needs.
func TestSettingsSpeakersDisableLiveKeepsMeeting(t *testing.T) {
	cfg := &config.Config{}
	cfg.Speaker.Enabled = true
	cfg.Speaker.Live.Enabled = true
	cfg.Speaker.Meeting.Enabled = true
	m, saved := newSpeakerSettings(t, cfg, speakerRowLive)

	m.selectCurrent()

	if saved["speaker.live.enabled"] != false {
		t.Errorf("speaker.live.enabled saved = %v, want false", saved["speaker.live.enabled"])
	}
	if _, ok := saved["speaker.enabled"]; ok {
		t.Errorf("disabling live must not write speaker.enabled, saved=%v", saved)
	}
	if !cfg.Speaker.Enabled || !cfg.Speaker.Meeting.Enabled {
		t.Errorf("cfg speaker = %+v, want meeting diarization intact", cfg.Speaker)
	}
}

// TestSettingsSpeakersEnableLiveKeepsExistingMaster covers the common path from
// the Meeting tab: master already on, so only the live key should be written.
func TestSettingsSpeakersEnableLiveKeepsExistingMaster(t *testing.T) {
	cfg := &config.Config{}
	cfg.Speaker.Enabled = true
	cfg.Speaker.Meeting.Enabled = true
	m, saved := newSpeakerSettings(t, cfg, speakerRowLive)

	m.selectCurrent()

	if _, ok := saved["speaker.enabled"]; ok {
		t.Errorf("master was already on; should not be rewritten: %v", saved)
	}
	if saved["speaker.live.enabled"] != true {
		t.Errorf("speaker.live.enabled saved = %v, want true", saved["speaker.live.enabled"])
	}
}

func TestSettingsSpeakersThresholdCycles(t *testing.T) {
	cfg := &config.Config{}
	cfg.Speaker.Live.Threshold = 0.55
	m, saved := newSpeakerSettings(t, cfg, speakerRowThreshold)

	m.selectCurrent()

	got, ok := saved["speaker.live.threshold"].(float32)
	if !ok {
		t.Fatalf("speaker.live.threshold saved = %#v, want float32", saved["speaker.live.threshold"])
	}
	if got != 0.6 {
		t.Errorf("threshold cycled to %v, want 0.60 (next after 0.55)", got)
	}
	if cfg.Speaker.Live.Threshold != got {
		t.Errorf("cfg threshold = %v, want %v", cfg.Speaker.Live.Threshold, got)
	}
}

// TestSettingsSpeakersThresholdCycleWraps proves the last choice returns to the
// first rather than sticking.
func TestSettingsSpeakersThresholdCycleWraps(t *testing.T) {
	cfg := &config.Config{}
	cfg.Speaker.Live.Threshold = 0.7
	m, saved := newSpeakerSettings(t, cfg, speakerRowThreshold)

	m.selectCurrent()

	if got := saved["speaker.live.threshold"]; got != float32(0.45) {
		t.Errorf("threshold after 0.70 = %v, want 0.45", got)
	}
}

// TestSettingsSpeakersThresholdInheritsRoot mirrors Normalize's inherit rule:
// an unset live threshold cycles from the root value, not from zero.
func TestSettingsSpeakersThresholdInheritsRoot(t *testing.T) {
	cfg := &config.Config{}
	cfg.Speaker.Threshold = 0.7
	m, saved := newSpeakerSettings(t, cfg, speakerRowThreshold)

	m.selectCurrent()

	if got := saved["speaker.live.threshold"]; got != float32(0.45) {
		t.Errorf("threshold = %v, want 0.45 (cycled from inherited 0.70)", got)
	}
}

func TestSettingsSpeakersWindowCycles(t *testing.T) {
	cfg := &config.Config{}
	m, saved := newSpeakerSettings(t, cfg, speakerRowWindow)

	// Unset window is the 1500ms default, so one press moves to 2000.
	m.selectCurrent()

	if got := saved["speaker.live.window_ms"]; got != 2000 {
		t.Errorf("window_ms saved = %v, want 2000", got)
	}
	if cfg.Speaker.Live.WindowMS != 2000 {
		t.Errorf("cfg window = %d, want 2000", cfg.Speaker.Live.WindowMS)
	}
}

// TestSettingsSpeakersMeetingRowIsReadOnly keeps ownership clear: meeting
// diarization is shown for orientation but changed in the Meeting tab.
func TestSettingsSpeakersMeetingRowIsReadOnly(t *testing.T) {
	cfg := &config.Config{}
	m, saved := newSpeakerSettings(t, cfg, speakerRowMeeting)

	m.selectCurrent()

	if len(saved) != 0 {
		t.Errorf("meeting row wrote config: %v", saved)
	}
	if !strings.Contains(m.message, "Meeting") {
		t.Errorf("message = %q, want a pointer to the Meeting tab", m.message)
	}
}

// TestSettingsSpeakersViewShowsState is the discoverability check — the user
// reported not finding any speakers setting in the TUI.
func TestSettingsSpeakersViewShowsState(t *testing.T) {
	cfg := &config.Config{}
	cfg.Speaker.Enabled = true
	cfg.Speaker.Live.Enabled = true
	cfg.Speaker.Meeting.Enabled = true
	m := newSettings(cfg, nil)
	m.section = sectionSpeakers

	view := stripANSI(m.View())

	for _, want := range []string{"Speakers", "Your name", "Speaker labels in chat", "Match threshold", "Analysis window", "Meeting diarization"} {
		if !strings.Contains(view, want) {
			t.Errorf("speakers view missing %q:\n%s", want, view)
		}
	}
}

// TestSpeakerItemsRowCountMatchesConstants keeps the row indices honest; a row
// added without updating the constants would silently misroute Enter.
func TestSpeakerItemsRowCountMatchesConstants(t *testing.T) {
	m := newSettings(&config.Config{}, nil)
	if got := len(m.speakerItems()); got != speakerControlRowCount {
		t.Errorf("speakerItems() has %d rows, speakerControlRowCount = %d", got, speakerControlRowCount)
	}
}

func TestSettingsSpeakersNameOpensEditorAndSaves(t *testing.T) {
	cfg := &config.Config{}
	m, saved := newSpeakerSettings(t, cfg, speakerRowName)

	if cmd := m.selectCurrent(); cmd == nil {
		t.Fatal("selecting Your name must open the editor")
	}
	if m.edit.kind != editUserDisplayName {
		t.Fatalf("edit kind = %v, want editUserDisplayName", m.edit.kind)
	}

	next := commitTyped(*m, "  Lance  ")
	if saved["user_display_name"] != "Lance" {
		t.Fatalf("saved = %v, want Lance", saved["user_display_name"])
	}
	if next.cfg.UserDisplayName != "Lance" {
		t.Fatalf("cfg name = %q", next.cfg.UserDisplayName)
	}
	if !strings.Contains(next.message, "Lance") {
		t.Fatalf("message = %q, want the saved name", next.message)
	}
}

func TestSpeakerRowsHideEnrollAnotherOnEmptyStore(t *testing.T) {
	m := newSettings(&config.Config{}, nil)
	m.section = sectionSpeakers
	rows := m.speakerRows()
	if len(rows) != speakerControlRowCount {
		t.Fatalf("empty store rows = %d, want %d", len(rows), speakerControlRowCount)
	}
	for _, row := range rows {
		if row.kind == speakerRowEnrollOther || row.kind == speakerRowGuest {
			t.Fatalf("empty store must not show roster rows: %+v", row)
		}
	}
	view := stripANSI(m.View())
	if strings.Contains(view, "Enroll another speaker") {
		t.Fatalf("empty store showed enroll another:\n%s", view)
	}
}

func TestSpeakerRowsShowGuestsAndEnrollAnother(t *testing.T) {
	m := newSettings(&config.Config{}, nil)
	m.section = sectionSpeakers
	m.enrolledOwner = speaker.Profile{Name: "Lance", Owner: true, ModelRev: speaker.ExpectedLiveRev(speaker.FromAppConfig(m.cfg))}
	m.enrolledOwnerOK = true
	m.enrolledProfiles = []speaker.Profile{
		m.enrolledOwner,
		{Name: "Alex", ModelRev: m.enrolledOwner.ModelRev},
		{Name: "Dana", ModelRev: "titanet-w9999"},
	}

	rows := m.speakerRows()
	if got, want := len(rows), speakerControlRowCount+1+2; got != want {
		t.Fatalf("rows = %d, want %d (controls + enroll another + 2 guests)", got, want)
	}
	if rows[speakerControlRowCount].kind != speakerRowEnrollOther {
		t.Fatalf("row %d = %+v, want enroll another", speakerControlRowCount, rows[speakerControlRowCount])
	}

	var guests []speakerRow
	for _, row := range rows {
		if row.kind == speakerRowGuest {
			guests = append(guests, row)
		}
	}
	if len(guests) != 2 {
		t.Fatalf("guest rows = %+v, want Alex and Dana", guests)
	}
	if guests[0].name != "Alex" || guests[1].name != "Dana" {
		t.Fatalf("guest order = %+v", guests)
	}
	if strings.Contains(guests[0].label, "re-enroll to refresh") {
		t.Errorf("fresh guest labelled stale: %q", guests[0].label)
	}
	if !strings.Contains(guests[1].label, "re-enroll to refresh") {
		t.Errorf("stale guest missing refresh hint: %q", guests[1].label)
	}
	for _, row := range guests {
		if row.name == "Lance" {
			t.Fatal("owner must not appear as a guest row")
		}
	}

	view := stripANSI(m.View())
	for _, want := range []string{"Enroll another speaker…", "Alex", "Dana (re-enroll to refresh)", "Re-enroll your voice — Lance"} {
		if !strings.Contains(view, want) {
			t.Errorf("view missing %q:\n%s", want, view)
		}
	}
}

func TestSpeakerRowsAfterOwnerRemovedStillListGuests(t *testing.T) {
	m := newSettings(&config.Config{}, nil)
	m.section = sectionSpeakers
	m.enrolledProfiles = []speaker.Profile{{Name: "Alex"}}

	labels := strings.Join(m.speakerItems(), "\n")
	if !strings.Contains(labels, "Register your voice") {
		t.Errorf("owner-removed labels missing Register:\n%s", labels)
	}
	if !strings.Contains(labels, "nothing registered") {
		t.Errorf("owner-removed labels missing empty owner copy:\n%s", labels)
	}
	if !strings.Contains(labels, "Enroll another speaker…") {
		t.Errorf("owner-removed labels missing enroll another:\n%s", labels)
	}
	if !strings.Contains(labels, "Alex") {
		t.Errorf("owner-removed labels missing guest:\n%s", labels)
	}
}

func TestSettingsSpeakersWindowWarnsWhenVoicesEnrolled(t *testing.T) {
	cfg := &config.Config{}
	m, saved := newSpeakerSettings(t, cfg, speakerRowWindow)
	m.enrolledProfiles = []speaker.Profile{{Name: "Lance", Owner: true}}

	m.selectCurrent()

	if saved["speaker.live.window_ms"] != 2000 {
		t.Errorf("window_ms saved = %v, want 2000", saved["speaker.live.window_ms"])
	}
	if !strings.Contains(m.message, "stale") {
		t.Errorf("message = %q, want a stale warning when voices are enrolled", m.message)
	}
}

func TestSettingsSpeakersNameClear(t *testing.T) {
	cfg := &config.Config{UserDisplayName: "Lance"}
	m, saved := newSpeakerSettings(t, cfg, speakerRowName)
	m.selectCurrent()
	next := commitTyped(*m, "")
	if saved["user_display_name"] != "" {
		t.Fatalf("cleared save = %#v", saved["user_display_name"])
	}
	if next.cfg.UserDisplayName != "" {
		t.Fatalf("cfg after clear = %q", next.cfg.UserDisplayName)
	}
	if !strings.Contains(next.message, "cleared") {
		t.Fatalf("message = %q, want cleared", next.message)
	}
}
