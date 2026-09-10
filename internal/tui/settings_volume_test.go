package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

// volumeLine is the Volume slider's line in the Audio section.
func volumeLine(m settingsModel) int {
	return lineOf(m, "playback_volume", -1)
}

func audioSettings(t *testing.T, cfg *config.Config) settingsModel {
	t.Helper()
	m := newSettings(cfg, nil)
	m.devicesLoading = false
	m.outputItems = []string{"", "Desk Speakers"}
	m.gotoSection(sectionAudio)
	return m
}

func TestAudioShowsDevicesAndVolumeSlider(t *testing.T) {
	m := audioSettings(t, &config.Config{PlaybackVolume: 1})
	m.width, m.height = 80, 20

	view := stripANSI(m.View())
	for _, want := range []string{"Microphone", "Speaker", "✓ System default", "Desk Speakers", "Volume", "100%"} {
		if !strings.Contains(view, want) {
			t.Fatalf("Audio missing %q:\n%s", want, view)
		}
	}
	// The slider is last, so arriving does not land on it.
	if def, vol := strings.Index(view, "Desk Speakers"), strings.Index(view, "Volume"); !(def < vol) {
		t.Fatalf("Audio lines out of order (speaker %d, volume %d):\n%s", def, vol, view)
	}
	if m.volumeRowFocused() {
		t.Fatal("arriving on Audio landed on the slider")
	}
}

// Section keys cross Audio without touching the slider or writing anything;
// end reaches the slider.
func TestCrossingAudioNeverWrites(t *testing.T) {
	m := audioSettings(t, &config.Config{PlaybackVolume: 1})
	m.gotoSection(sectionSpeakers)
	m.saveConfig = func(key string, value any) error {
		t.Fatalf("crossing Audio wrote %s=%v", key, value)
		return nil
	}

	m, _ = updateSettingsWithKey(t, m, "right")
	if m.section != sectionAudio || m.volumeRowFocused() {
		t.Fatalf("after →: section %v volume focused %v; want Audio on a device", m.section, m.volumeRowFocused())
	}
	m, _ = updateSettingsWithKey(t, m, "right")
	if m.section != sectionMeeting || m.cfg.PlaybackVolume != 1 {
		t.Fatalf("second →: section %v volume %v; want Meeting and volume 1", m.section, m.cfg.PlaybackVolume)
	}

	m, _ = updateSettingsWithKey(t, m, "left")
	m = pressSettingsKey(m, tea.KeyEnd)
	if !m.volumeRowFocused() {
		t.Fatalf("cursor %d after end is not the slider (want %d)", m.cursor, volumeLine(m))
	}
}

// While devices are still being discovered the device rows are disabled:
// enter explains and cannot write.
func TestDeviceRowsWaitForDevices(t *testing.T) {
	m := newSettings(&config.Config{PlaybackVolume: 1}, nil)
	m.devicesLoading = true
	m.gotoSection(sectionAudio)
	m.saveConfig = func(key string, value any) error {
		t.Fatalf("wrote %s=%v while devices were loading", key, value)
		return nil
	}
	if m.volumeRowFocused() {
		t.Fatal("slider focused while devices load")
	}
	m = pressSettingsKey(m, tea.KeyEnter)
	if m.section != sectionAudio || m.cfg.PlaybackVolume != 1 || !strings.Contains(m.message, "Discovering") {
		t.Fatalf("enter while loading: section %v volume %v message %q", m.section, m.cfg.PlaybackVolume, m.message)
	}
}

func TestVolumeSliderNudgesAndPersists(t *testing.T) {
	saved := map[string]any{}
	var applied []float64
	m := audioSettings(t, &config.Config{PlaybackVolume: 1})
	m.cursor = volumeLine(m)
	m.saveConfig = func(key string, value any) error {
		saved[key] = value
		return nil
	}
	m.onPlaybackVolume = func(gain float64) { applied = append(applied, gain) }

	m = typeIntoSettings(m, "-")
	if m.section != sectionAudio {
		t.Fatal("- on the volume row switched sections")
	}
	if m.cfg.PlaybackVolume != 0.95 {
		t.Fatalf("volume after - = %v, want 0.95", m.cfg.PlaybackVolume)
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

	m = typeIntoSettings(m, "+")
	if m.cfg.PlaybackVolume != 1 {
		t.Fatalf("volume after + = %v, want 1", m.cfg.PlaybackVolume)
	}

	m = typeIntoSettings(m, "_")
	if m.cfg.PlaybackVolume != 0.95 {
		t.Fatalf("_ should nudge volume down, got %v", m.cfg.PlaybackVolume)
	}
	m = typeIntoSettings(m, "=")
	if m.cfg.PlaybackVolume != 1 {
		t.Fatalf("= should nudge volume up, got %v", m.cfg.PlaybackVolume)
	}
}

func TestVolumeSliderTabStillChangesSection(t *testing.T) {
	m := audioSettings(t, &config.Config{PlaybackVolume: 1})
	m.cursor = volumeLine(m)

	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})
	if m.section != sectionMeeting {
		t.Fatalf("tab from Audio = %v, want Meeting", m.section)
	}
}

// enter on a speaker chooses it — never the volume, never the section.
func TestEnterOnASpeakerChoosesIt(t *testing.T) {
	saved := map[string]any{}
	m := audioSettings(t, &config.Config{PlaybackVolume: 1})
	m.saveConfig = func(key string, value any) error {
		saved[key] = value
		return nil
	}
	m.cursor = lineOf(m, "output_device", 1)
	if m.cursor < 0 {
		t.Fatal("no line for Desk Speakers")
	}

	m = pressSettingsKey(m, tea.KeyEnter)
	if m.section != sectionAudio {
		t.Fatal("enter on a speaker switched sections")
	}
	if saved["output_device"] != "Desk Speakers" || m.cfg.OutputDevice != "Desk Speakers" {
		t.Fatalf("enter did not choose the speaker: saved %v cfg %q", saved["output_device"], m.cfg.OutputDevice)
	}
	if _, wrote := saved["playback_volume"]; wrote || m.cfg.PlaybackVolume != 1 {
		t.Fatalf("speaker touched the volume: saved %v cfg %v", saved["playback_volume"], m.cfg.PlaybackVolume)
	}

	m.cursor = lineOf(m, "output_device", 0)
	m = pressSettingsKey(m, tea.KeyEnter)
	if m.cfg.OutputDevice != "" {
		t.Fatalf("enter did not go back to the system default: %q", m.cfg.OutputDevice)
	}
}

func TestVolumeSliderClampsAtZero(t *testing.T) {
	writes := 0
	m := audioSettings(t, &config.Config{PlaybackVolume: 0})
	m.cursor = volumeLine(m)
	m.saveConfig = func(string, any) error {
		writes++
		return nil
	}

	m = typeIntoSettings(m, "-")
	if m.section != sectionAudio {
		t.Fatal("- at mute switched sections")
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
	m := audioSettings(t, &config.Config{PlaybackVolume: 1, OutputDevice: ""})
	m.cursor = volumeLine(m)
	m.saveConfig = func(key string, value any) error {
		t.Fatalf("enter on volume wrote %s=%v", key, value)
		return nil
	}
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if m.cfg.OutputDevice != "" {
		t.Fatalf("output device = %q, want unchanged", m.cfg.OutputDevice)
	}
}
