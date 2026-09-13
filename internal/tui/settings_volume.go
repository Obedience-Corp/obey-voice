package tui

import (
	"fmt"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio/playback"
	"math"
	"strings"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

const (
	playbackVolumeStep = 0.05
	playbackVolumeBarW = 20
)

func (m settingsModel) playbackVolume() float64 {
	if m.cfg == nil {
		return config.DefaultPlaybackVolume
	}
	return config.PlaybackVolumeOf(m.cfg)
}

// volumeValue is the slider as the Volume row shows it.
func (m settingsModel) volumeValue() string {
	gain := m.playbackVolume()
	return fmt.Sprintf("%s  %d%%", volumeBar(gain, playbackVolumeBarW), int(math.Round(gain*100)))
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

// adjustVolume moves the playback gain by delta, clamped, saves it, and
// applies it to the preview player and the live conversation. It is the
// Volume row's +/- and nothing else: the row, not the key, decides.
func (m *settingsModel) adjustVolume(delta float64) {
	if m.cfg == nil {
		return
	}
	next := clampPlaybackVolume(m.cfg.PlaybackVolume + delta)
	if next == m.cfg.PlaybackVolume && delta != 0 {
		return
	}
	if err := m.save("playback_volume", next); err != nil {
		m.message = fmt.Sprintf("Failed to save volume: %v", err)
		return
	}
	m.cfg.PlaybackVolume = next
	playback.ApplyEngineVolume(m.preview.previewPlayer, next)
	if m.onPlaybackVolume != nil {
		m.onPlaybackVolume(next)
	}
	m.message = fmt.Sprintf("Volume %d%%", int(math.Round(next*100)))
}

// volumeRowFocused reports whether the cursor is on the Volume slider.
func (m settingsModel) volumeRowFocused() bool {
	row, ok := m.focusedRow()
	return ok && row.kind == rowSlider && row.key == "playback_volume"
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
