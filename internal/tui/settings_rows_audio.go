package tui

import (
	tea "github.com/charmbracelet/bubbletea"
)

func deviceOptions(items []string) []string {
	out := make([]string, len(items))
	for i, item := range items {
		out[i] = deviceLabel(item)
	}
	return out
}

func (m settingsModel) inputRows() []settingRow {
	row := settingRow{
		key: "input_device", name: "Microphone", kind: rowPick,
		applies: "applies when returning to the conversation",
		help:    "Which microphone it listens to.",
	}
	if m.devicesLoading {
		row.disabled = true
		row.reason = "Discovering audio devices…"
		return []settingRow{row}
	}
	devices := m.inputItems
	row.value = deviceLabel(m.cfg.InputDevice)
	row.options = deviceOptions(devices)
	row.current = indexOf(devices, m.cfg.InputDevice)
	row.set = func(m *settingsModel, i int) tea.Cmd { return m.selectInputDevice(devices[i]) }
	return []settingRow{row}
}

func (m settingsModel) outputRows() []settingRow {
	speaker := settingRow{
		key: "output_device", name: "Speaker", kind: rowPick,
		applies: appliesNextConversation,
		help:    "Which loudspeaker it comes out of. Previews use it now.",
	}
	if m.devicesLoading {
		speaker.disabled = true
		speaker.reason = "Discovering audio devices…"
	} else {
		devices := m.outputItems
		speaker.value = deviceLabel(m.cfg.OutputDevice)
		speaker.options = deviceOptions(devices)
		speaker.current = indexOf(devices, m.cfg.OutputDevice)
		speaker.set = func(m *settingsModel, i int) tea.Cmd { return m.selectOutputDevice(devices[i]) }
	}
	volume := settingRow{
		key: "playback_volume", name: "Volume", kind: rowSlider,
		value: m.volumeValue(), applies: appliesNow,
		help: "Playback level for the conversation and for previews.",
		nudge: func(m *settingsModel, delta int) tea.Cmd {
			m.adjustVolume(float64(delta) * playbackVolumeStep)
			return nil
		},
	}
	return []settingRow{speaker, volume}
}
