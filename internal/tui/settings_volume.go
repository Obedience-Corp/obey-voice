package tui

import (
	"fmt"
	"math"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

const (
	playbackVolumeStep = 0.05
	playbackVolumeBarW = 20
)

// volumeRow is the last row of the Output tab, below every speaker. Arriving
// on the tab therefore lands on System default, and ←/→ keep switching
// sections until the user goes down to the slider on purpose; with the slider
// first, the same keys silently changed the volume instead of the tab.
func (m settingsModel) volumeRow() int {
	if m.devicesLoading {
		return 0
	}
	return len(m.outputItems)
}

// volumeRowFocused is false while devices are still loading: the slider is
// drawn, but ←/→ stay section keys until the list it sits under exists.
func (m settingsModel) volumeRowFocused() bool {
	return m.section == sectionOutput && !m.devicesLoading && m.cursor == m.volumeRow()
}

func (m settingsModel) playbackVolume() float64 {
	if m.cfg == nil {
		return config.DefaultPlaybackVolume
	}
	return config.PlaybackVolumeOf(m.cfg)
}

func (m settingsModel) volumeRowLabel() string {
	gain := m.playbackVolume()
	pct := int(math.Round(gain * 100))
	return fmt.Sprintf("Volume  %s  %d%%", volumeBar(gain, playbackVolumeBarW), pct)
}

func volumeBar(gain float64, width int) string {
	if width <= 0 {
		return ""
	}
	fill := int(math.Round(gain * float64(width)))
	if fill < 0 {
		fill = 0
	}
	if fill > width {
		fill = width
	}
	return strings.Repeat("█", fill) + strings.Repeat("░", width-fill)
}

func (m settingsModel) outputLines() []string {
	total := m.currentListLen()
	start, end := m.visibleRange(total)
	lines := make([]string, 0, end-start+1)
	for i := start; i < end; i++ {
		lines = append(lines, m.itemLine(i, m.outputRowLabel(i)))
	}
	if m.devicesLoading {
		lines = append(lines, dimStyle.Render("  Discovering audio devices..."))
	}
	return lines
}

func (m settingsModel) outputRowLabel(idx int) string {
	if idx == m.volumeRow() {
		return m.volumeRowLabel()
	}
	if m.devicesLoading || idx < 0 || idx >= len(m.outputItems) {
		return ""
	}
	item := m.outputItems[idx]
	label := deviceLabel(item)
	if m.cfg != nil && item == m.cfg.OutputDevice {
		label += " ✓"
	}
	return label
}

func (m *settingsModel) switchSettingsSection(delta int) tea.Cmd {
	n := int(settingsSectionCount)
	m.section = settingsSection((int(m.section) + delta%n + n) % n)
	m.cursor = 0
	m.offset = 0
	m.message = ""
	if m.section == sectionMeeting && !m.routeDestsLoading && m.routeDests == nil {
		return m.loadRouteDestinations()
	}
	return nil
}

func (m *settingsModel) nudgeVolume(delta float64) bool {
	if !m.volumeRowFocused() || m.cfg == nil {
		return false
	}
	next := clampPlaybackVolume(m.cfg.PlaybackVolume + delta)
	if next == m.cfg.PlaybackVolume && delta != 0 {
		// Already at a bound; still handled so left/right don't change tabs.
		return true
	}
	if err := m.save("playback_volume", next); err != nil {
		m.message = fmt.Sprintf("Failed to save volume: %v", err)
		return true
	}
	m.cfg.PlaybackVolume = next
	audio.ApplyEngineVolume(m.preview.previewPlayer, next)
	if m.onPlaybackVolume != nil {
		m.onPlaybackVolume(next)
	}
	m.message = fmt.Sprintf("Volume %d%%", int(math.Round(next*100)))
	return true
}

func clampPlaybackVolume(gain float64) float64 {
	gain = math.Round(gain*100) / 100
	if gain < config.MinPlaybackVolume {
		return config.MinPlaybackVolume
	}
	if gain > config.MaxPlaybackVolume {
		return config.MaxPlaybackVolume
	}
	return gain
}
