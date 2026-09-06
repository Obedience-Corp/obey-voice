package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

func TestOutputTabShowsVolumeSlider(t *testing.T) {
	m := newSettings(&config.Config{PlaybackVolume: 1}, nil)
	m.section = sectionOutput
	m.width, m.height = 80, 20
	m.devicesLoading = false
	m.outputItems = []string{"", "Desk Speakers"}

	view := stripANSI(m.View())
	if !strings.Contains(view, "Volume") || !strings.Contains(view, "100%") {
		t.Fatalf("output tab missing volume slider:\n%s", view)
	}
	if !strings.Contains(view, "Desk Speakers") {
		t.Fatalf("output tab dropped speaker list:\n%s", view)
	}
}

func TestVolumeSliderNudgesAndPersists(t *testing.T) {
	saved := map[string]any{}
	var applied []float64
	m := newSettings(&config.Config{PlaybackVolume: 1}, nil)
	m.section = sectionOutput
	m.cursor = outputRowVolume
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
	m := newSettings(&config.Config{PlaybackVolume: 1}, nil)
	m.section = sectionOutput
	m.cursor = outputRowVolume

	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})
	if m.section != sectionMeeting {
		t.Fatalf("tab from Output = %v, want Meeting", m.section)
	}
}

func TestVolumeSliderDoesNotStealLeftWhenOnSpeaker(t *testing.T) {
	m := newSettings(&config.Config{PlaybackVolume: 1}, nil)
	m.section = sectionOutput
	m.outputItems = []string{"", "Desk Speakers"}
	m.cursor = 1 // system default device row
	m.saveConfig = func(string, any) error {
		t.Fatal("left on a speaker row must not write volume")
		return nil
	}

	m, _ = updateSettingsWithKey(t, m, "left")
	if m.section == sectionOutput {
		t.Fatal("left on a speaker row should switch tabs")
	}
	if m.cfg.PlaybackVolume != 1 {
		t.Fatalf("volume changed to %v", m.cfg.PlaybackVolume)
	}
}

func TestVolumeSliderClampsAtZero(t *testing.T) {
	writes := 0
	m := newSettings(&config.Config{PlaybackVolume: 0}, nil)
	m.section = sectionOutput
	m.cursor = outputRowVolume
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
	m := newSettings(&config.Config{PlaybackVolume: 1, OutputDevice: ""}, nil)
	m.section = sectionOutput
	m.cursor = outputRowVolume
	m.outputItems = []string{"", "Desk Speakers"}
	m.saveConfig = func(key string, value any) error {
		t.Fatalf("enter on volume wrote %s=%v", key, value)
		return nil
	}
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if m.cfg.OutputDevice != "" {
		t.Fatalf("output device = %q, want unchanged", m.cfg.OutputDevice)
	}
}
