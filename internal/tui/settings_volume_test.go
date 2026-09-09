package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

// volumeRowIndex is the Volume slider's row in the Output section.
func volumeRowIndex(m settingsModel) int {
	for i, row := range m.sectionRows() {
		if row.key == "playback_volume" {
			return i
		}
	}
	return -1
}

func outputSettings(t *testing.T, cfg *config.Config) settingsModel {
	t.Helper()
	m := newSettings(cfg, nil)
	m.section = sectionOutput
	m.devicesLoading = false
	m.outputItems = []string{"", "Desk Speakers"}
	return m
}

func TestOutputTabShowsVolumeSlider(t *testing.T) {
	m := outputSettings(t, &config.Config{PlaybackVolume: 1})
	m.width, m.height = 80, 20

	view := stripANSI(m.View())
	for _, want := range []string{"Speaker", "System default", "Volume", "100%"} {
		if !strings.Contains(view, want) {
			t.Fatalf("output tab missing %q:\n%s", want, view)
		}
	}
	// The slider is the second row, so arriving on the tab does not land on it.
	if def, vol := strings.Index(view, "System default"), strings.Index(view, "Volume"); !(def < vol) {
		t.Fatalf("output rows out of order (speaker %d, volume %d):\n%s", def, vol, view)
	}
	// The other speakers live in the picker, not on the tab.
	if strings.Contains(view, "Desk Speakers") {
		t.Fatalf("output tab lists devices outside the picker:\n%s", view)
	}
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if view := stripANSI(m.View()); !strings.Contains(view, "Desk Speakers") || !strings.Contains(view, "System default ✓") {
		t.Fatalf("speaker picker missing the devices or the mark:\n%s", view)
	}
}

// Coming onto the Output tab lands on the speaker row, and the section keys
// never touch the slider or write anything.
func TestArrivingOnOutputLandsOnTheSpeakerRow(t *testing.T) {
	m := outputSettings(t, &config.Config{PlaybackVolume: 1})
	m.section = sectionInput
	m.saveConfig = func(key string, value any) error {
		t.Fatalf("crossing the Output tab wrote %s=%v", key, value)
		return nil
	}

	m, _ = updateSettingsWithKey(t, m, "tab")
	if m.section != sectionOutput || m.cursor != 0 || m.volumeRowFocused() {
		t.Fatalf("after tab: section %v cursor %d volume focused %v; want Output, row 0, speaker", m.section, m.cursor, m.volumeRowFocused())
	}
	m, _ = updateSettingsWithKey(t, m, "tab")
	if m.section != sectionMeeting || m.cfg.PlaybackVolume != 1 {
		t.Fatalf("second tab: section %v volume %v; want Meeting and volume 1", m.section, m.cfg.PlaybackVolume)
	}

	m, _ = updateSettingsWithKey(t, m, "shift+tab")
	m, _ = updateSettingsWithKey(t, m, "down")
	if !m.volumeRowFocused() {
		t.Fatalf("cursor %d after down is not the slider (want %d)", m.cursor, volumeRowIndex(m))
	}
}

// While devices are still being discovered the speaker row is disabled: ←/→
// do nothing there — they no longer switch section, and they cannot write.
func TestSpeakerRowWaitsForDevices(t *testing.T) {
	m := newSettings(&config.Config{PlaybackVolume: 1}, nil)
	m.section = sectionOutput
	m.devicesLoading = true
	m.cursor = 0
	m.saveConfig = func(key string, value any) error {
		t.Fatalf("wrote %s=%v while devices were loading", key, value)
		return nil
	}
	if m.volumeRowFocused() {
		t.Fatal("slider focused while devices load")
	}
	m, _ = updateSettingsWithKey(t, m, "left")
	if m.section != sectionOutput || m.cfg.PlaybackVolume != 1 {
		t.Fatalf("left while loading: section %v volume %v; want Output untouched", m.section, m.cfg.PlaybackVolume)
	}
}

func TestVolumeSliderNudgesAndPersists(t *testing.T) {
	saved := map[string]any{}
	var applied []float64
	m := outputSettings(t, &config.Config{PlaybackVolume: 1})
	m.cursor = volumeRowIndex(m)
	m.saveConfig = func(key string, value any) error {
		saved[key] = value
		return nil
	}
	m.onPlaybackVolume = func(gain float64) { applied = append(applied, gain) }

	m, _ = updateSettingsWithKey(t, m, "left")
	if m.section != sectionOutput {
		t.Fatal("left on the volume row switched settings tabs")
	}
	if m.cfg.PlaybackVolume != 0.95 {
		t.Fatalf("volume after left = %v, want 0.95", m.cfg.PlaybackVolume)
	}
	if saved["playback_volume"] != 0.95 {
		t.Fatalf("saved playback_volume = %v, want 0.95", saved["playback_volume"])
	}
	if len(applied) != 1 || applied[0] != 0.95 {
		t.Fatalf("live apply = %v, want [0.95]", applied)
	}
	if m.message != "Volume 95%" {
		t.Fatalf("message = %q, want Volume 95%%", m.message)
	}

	m, _ = updateSettingsWithKey(t, m, "right")
	if m.cfg.PlaybackVolume != 1 {
		t.Fatalf("volume after right = %v, want 1", m.cfg.PlaybackVolume)
	}

	m, _ = updateSettingsWithKey(t, m, "h")
	if m.cfg.PlaybackVolume != 0.95 {
		t.Fatalf("h should nudge volume, got %v", m.cfg.PlaybackVolume)
	}
	m, _ = updateSettingsWithKey(t, m, "l")
	if m.cfg.PlaybackVolume != 1 {
		t.Fatalf("l should nudge volume, got %v", m.cfg.PlaybackVolume)
	}
}

func TestVolumeSliderTabStillChangesSection(t *testing.T) {
	m := outputSettings(t, &config.Config{PlaybackVolume: 1})
	m.cursor = volumeRowIndex(m)

	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})
	if m.section != sectionMeeting {
		t.Fatalf("tab from Output = %v, want Meeting", m.section)
	}
}

// ←/→ on the speaker row change the speaker — never the volume, never the
// section.
func TestArrowsOnTheSpeakerRowChangeTheSpeaker(t *testing.T) {
	saved := map[string]any{}
	m := outputSettings(t, &config.Config{PlaybackVolume: 1})
	m.cursor = 0
	m.saveConfig = func(key string, value any) error {
		saved[key] = value
		return nil
	}

	m, _ = updateSettingsWithKey(t, m, "right")
	if m.section != sectionOutput {
		t.Fatal("right on the speaker row switched sections")
	}
	if saved["output_device"] != "Desk Speakers" || m.cfg.OutputDevice != "Desk Speakers" {
		t.Fatalf("right did not choose the next speaker: saved %v cfg %q", saved["output_device"], m.cfg.OutputDevice)
	}
	if _, wrote := saved["playback_volume"]; wrote || m.cfg.PlaybackVolume != 1 {
		t.Fatalf("speaker row touched the volume: saved %v cfg %v", saved["playback_volume"], m.cfg.PlaybackVolume)
	}

	m, _ = updateSettingsWithKey(t, m, "left")
	if m.cfg.OutputDevice != "" {
		t.Fatalf("left did not step back to the system default: %q", m.cfg.OutputDevice)
	}
}

func TestVolumeSliderClampsAtZero(t *testing.T) {
	writes := 0
	m := outputSettings(t, &config.Config{PlaybackVolume: 0})
	m.cursor = volumeRowIndex(m)
	m.saveConfig = func(string, any) error {
		writes++
		return nil
	}

	m, _ = updateSettingsWithKey(t, m, "left")
	if m.section != sectionOutput {
		t.Fatal("left at mute switched tabs")
	}
	if m.cfg.PlaybackVolume != 0 {
		t.Fatalf("volume = %v, want 0", m.cfg.PlaybackVolume)
	}
	if writes != 0 {
		t.Fatalf("clamped nudge wrote config %d times", writes)
	}
}

func TestVolumeBarFillsToUnity(t *testing.T) {
	if got := volumeBar(0, 4); got != "░░░░" {
		t.Fatalf("0 = %q", got)
	}
	if got := volumeBar(1, 4); got != "████" {
		t.Fatalf("1 = %q", got)
	}
	if got := volumeBar(0.5, 4); got != "██░░" {
		t.Fatalf("0.5 = %q", got)
	}
	if got := volumeBar(2, 4); got != "████" {
		t.Fatalf("boost stays full = %q", got)
	}
}

func TestEnterOnVolumeRowDoesNotSelectSpeaker(t *testing.T) {
	m := outputSettings(t, &config.Config{PlaybackVolume: 1, OutputDevice: ""})
	m.cursor = volumeRowIndex(m)
	m.saveConfig = func(key string, value any) error {
		t.Fatalf("enter on volume wrote %s=%v", key, value)
		return nil
	}
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if m.cfg.OutputDevice != "" {
		t.Fatalf("output device = %q, want unchanged", m.cfg.OutputDevice)
	}
}
