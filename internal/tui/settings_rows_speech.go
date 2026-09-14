package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

func (m settingsModel) sttRows() []settingRow {
	active := activeSTTProvider(m.cfg)
	items := m.sttItems
	options := make([]string, len(items))
	current := -1
	for i, it := range items {
		options[i] = it.label
		if it.provider == active {
			current = i
		}
	}
	value := active
	if value == "" {
		value = "not set"
	}
	return []settingRow{{
		key: "stt_provider", name: "Recognizer", kind: rowPick,
		value: value, detail: sttConfiguredAliasNote(m.cfg), options: options, current: current,
		applies: "applies to a new conversation",
		help:    "Which speech recognizer hears you.",
		set:     func(m *settingsModel, i int) tea.Cmd { return m.selectSTTItem(items[i]) },
	}}
}

func (m settingsModel) speakerSettingRows() []settingRow {
	thresholds := make([]string, len(liveThresholdChoices))
	for i, t := range liveThresholdChoices {
		thresholds[i] = fmt.Sprintf("%.2f", t)
	}
	windows := make([]string, len(liveWindowChoices))
	for i, w := range liveWindowChoices {
		windows[i] = fmt.Sprintf("%d ms", w)
	}
	sp := m.cfg.Speaker
	var rows []settingRow
	for _, r := range m.speakerRows() {
		r := r
		run := func(m *settingsModel) tea.Cmd { return m.selectSpeakerRow(r) }
		var row settingRow
		switch r.kind {
		case speakerRowName:
			name := strings.TrimSpace(m.cfg.UserDisplayName)
			if name == "" {
				name = "not set"
			}
			row = settingRow{
				key: "user_display_name", name: "Your name", kind: rowText, value: name,
				applies: appliesNextConversation,
				help:    "Used instead of speaker-1 in chat labels and transcripts.",
				run:     run,
			}
		case speakerRowLive:
			row = settingRow{
				key: "speaker.live.enabled", name: "Speaker labels in chat", kind: rowToggle,
				value: onOff(sp.Enabled && sp.Live.Enabled), applies: appliesNextConversation,
				help: "Label who is talking in a conversation.",
				run:  run,
			}
		case speakerRowThreshold:
			current := m.effectiveLiveThreshold()
			row = settingRow{
				key: "speaker.live.threshold", name: "Match threshold", kind: rowChoice,
				value: fmt.Sprintf("%.2f", current), options: thresholds, current: nearestIndex32(liveThresholdChoices, current),
				applies: appliesNextConversation,
				help:    "Lower merges similar voices onto one label; higher splits them.",
				set: func(m *settingsModel, i int) tea.Cmd {
					m.setLiveThreshold(liveThresholdChoices[i])
					return nil
				},
			}
		case speakerRowWindow:
			window := sp.Live.WindowMS
			if window <= 0 {
				window = 1500
			}
			row = settingRow{
				key: "speaker.live.window_ms", name: "Analysis window", kind: rowChoice,
				value: fmt.Sprintf("%d ms", window), options: windows, current: indexOf(windows, fmt.Sprintf("%d ms", window)),
				applies: appliesNextConversation,
				help:    "Shorter labels a turn sooner; longer gives the embedder more voice. Enrolled voices go stale.",
				set: func(m *settingsModel, i int) tea.Cmd {
					m.setLiveWindow(liveWindowChoices[i])
					return nil
				},
			}
		case speakerRowMeeting:
			row = settingRow{
				name: "Meeting diarization", kind: rowInfo,
				value: onOff(sp.Enabled && sp.Meeting.Enabled), detail: "set in the Meeting tab",
				verb: "where", help: "Per-meeting; toggle it under Meeting.",
				run: run,
			}
		case speakerRowRegister:
			row = settingRow{
				name: "Register your voice", kind: rowAction, verb: "record",
				help: "Records three short clips so live labels know your voice. The microphone is live while it runs.",
				run:  run,
			}
			if m.enrolledOwnerOK {
				row.name, row.value = "Re-enroll your voice", m.enrolledOwner.Name
				if m.ownerRevIsStale() {
					row.detail = "re-enroll to refresh"
				}
			}
		case speakerRowRemove:
			row = settingRow{
				name: "Remove your voice", kind: rowAction, verb: "remove", value: "nothing registered",
				help: "Deletes your stored voiceprint. Asks first.",
				run:  run,
			}
			if m.enrolledOwnerOK {
				row.value = m.enrolledOwner.Name
			}
		case speakerRowEnrollOther:
			row = settingRow{
				name: "Enroll another speaker…", kind: rowAction, verb: "name them",
				help: "Records a guest's voice under a name you type.",
				run:  run,
			}
		case speakerRowGuest:
			row = settingRow{
				name: r.name, kind: rowAction, verb: "re-enroll",
				help: "Records this speaker again.",
				run:  run,
			}
			if strings.Contains(r.label, "re-enroll to refresh") {
				row.detail = "re-enroll to refresh"
			}
		default:
			continue
		}
		rows = append(rows, row)
	}
	var guests []string
	for _, p := range m.enrolledProfiles {
		if !p.Owner {
			guests = append(guests, p.Name)
		}
	}
	if len(guests) > 0 {
		rows = append(rows, settingRow{
			name: "Remove an enrolled speaker…", kind: rowMenu, verb: "choose",
			options: guests, current: -1,
			help: "Deletes that speaker's voiceprint. Asks first.",
			set: func(m *settingsModel, i int) tea.Cmd {
				m.beginRemoveVoiceNamed(guests[i])
				return nil
			},
		})
	}
	return rows
}

func nearestIndex32(choices []float32, value float32) int {
	idx := -1
	best := float32(-1)
	for i, choice := range choices {
		delta := choice - value
		if delta < 0 {
			delta = -delta
		}
		if best < 0 || delta < best {
			best, idx = delta, i
		}
	}
	return idx
}
