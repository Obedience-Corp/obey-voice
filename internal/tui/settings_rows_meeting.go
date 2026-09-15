package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

func (m settingsModel) meetingRows() []settingRow {
	cfg := m.cfg
	run := func(row int) func(*settingsModel) tea.Cmd {
		return func(m *settingsModel) tea.Cmd {
			m.selectMeetingRow(row)
			return nil
		}
	}
	diarizationOn := cfg.Speaker.Enabled && cfg.Speaker.Meeting.Enabled
	liveOn := cfg.Speaker.Enabled && cfg.Speaker.Meeting.Live
	engine, engineErr := config.MeetingDiarizerFrom(cfg)
	engines := config.MeetingDiarizers()
	engineLabels := make([]string, len(engines))
	for i, e := range engines {
		switch e {
		case config.DiarizerSortformer:
			engineLabels[i] = e + " — NVIDIA, through transcribe.cpp (optional)"
		default:
			engineLabels[i] = e + " — pyannote + TitaNet (default)"
		}
	}
	engineRow := settingRow{
		key: "speaker.meeting.diarizer", name: "Diarization engine", kind: rowPick,
		value: engine, options: engineLabels, current: indexOf(engines, engine),
		applies: appliesNextMeeting,
		help:    "Which engine labels speakers in the review pass. Live labels stay on sherpa.",
		set:     func(m *settingsModel, i int) tea.Cmd { return m.setMeetingDiarizer(engines[i]) },
	}
	if engineErr != nil {
		engineRow.value = strings.TrimSpace(cfg.Speaker.Meeting.Diarizer)
		engineRow.detail = "unknown value"
		engineRow.current = -1
	} else if engine == config.DiarizerSortformer {
		if gap := m.meetingDiarizerGap(); gap.Missing() {
			engineRow.detail = gap.Detail
		}
	}
	numLabels := make([]string, len(meetingNumSpeakerChoices))
	numCurrent := -1
	for i, n := range meetingNumSpeakerChoices {
		numLabels[i] = "Auto"
		if n > 0 {
			numLabels[i] = fmt.Sprintf("%d", n)
		}
		if n == cfg.Speaker.Meeting.NumSpeakers {
			numCurrent = i
		}
	}
	modes := []string{"ask", "auto", "off"}
	mode := cfg.Meeting.Route.Mode
	if mode == "" {
		mode = "ask"
	}
	bodies := []string{"notes", "full"}
	body := cfg.Meeting.Route.Body
	if body == "" {
		body = "notes"
	}
	dests := m.defaultDestinationChoices()
	destLabels := make([]string, len(dests))
	destCurrent := -1
	for i, d := range dests {
		destLabels[i] = d.id
		if d.id == "" {
			destLabels[i] = "(launch campaign)"
		}
		if d.id == cfg.Meeting.Route.Default {
			destCurrent = i
		}
	}
	destValue := cfg.Meeting.Route.Default
	if destValue == "" {
		destValue = "(launch campaign)"
	}
	availLabel := fmt.Sprintf("%d available", len(m.routeDests))
	if m.routeDestsLoading {
		availLabel = "discovering…"
	} else if m.routeDestsErr != "" {
		availLabel = "discovery error"
	}
	return []settingRow{
		{
			key: "speaker.meeting.enabled", name: "Speaker diarization", kind: rowToggle,
			value: onOff(diarizationOn), applies: appliesNextMeeting,
			help: "Label who said what in the review pass after a meeting.",
			run:  run(meetingRowDiarization),
		},
		{
			key: "speaker.meeting.live", name: "Live labels while recording", kind: rowToggle,
			value: onOff(liveOn), applies: appliesNextMeeting,
			help: "Provisional speaker-N labels while a meeting records.",
			run:  run(meetingRowLiveLabels),
		},
		engineRow,
		{
			key: "speaker.meeting.num_speakers", name: "Expected speakers", kind: rowChoice,
			value:   meetingNumSpeakersLabel(cfg.Speaker.Meeting.NumSpeakers, engine),
			options: numLabels, current: numCurrent, applies: appliesNextMeeting,
			help: "Fix the speaker count for 1:1 interviews and small groups; Auto can over-split.",
			set:  func(m *settingsModel, i int) tea.Cmd { return m.setMeetingNumSpeakers(meetingNumSpeakerChoices[i]) },
		},
		{
			key: "speaker.meeting.record_audio", name: "Record audio for analysis", kind: rowToggle,
			value: onOff(cfg.Speaker.Meeting.RecordAudio), applies: appliesNextMeeting,
			help: "Keep the meeting audio so the review pass can run again.",
			run:  run(meetingRowRecordAudio),
		},
		{
			key: "meeting.route.mode", name: "Route mode", kind: rowChoice,
			value: mode, options: modes, current: indexOf(modes, mode), applies: appliesNextMeeting,
			help: "ask: choose where notes go at the end · auto: send to the default · off: keep them here.",
			set:  func(m *settingsModel, i int) tea.Cmd { return m.setRouteMode(modes[i]) },
		},
		{
			key: "meeting.route.default", name: "Default destination", kind: rowPick,
			value: destValue, options: destLabels, current: destCurrent, applies: appliesNextMeeting,
			help: "Where notes go when route mode is auto.",
			set:  func(m *settingsModel, i int) tea.Cmd { return m.setDefaultDestinationChoice(dests[i]) },
		},
		{
			key: "meeting.route.body", name: "Body scope", kind: rowChoice,
			value: body, options: bodies, current: indexOf(bodies, body), applies: appliesNextMeeting,
			help: "notes: the summary · full: the whole transcript.",
			set:  func(m *settingsModel, i int) tea.Cmd { return m.setBodyScope(bodies[i]) },
		},
		{
			name: "Configured destinations", kind: rowInfo, verb: "where",
			value: fmt.Sprintf("%d", len(cfg.Meeting.Route.Destinations)),
			help:  "Destinations written in the config file.",
			run:   run(meetingRowConfigured),
		},
		{
			name: "Picker destinations", kind: rowInfo, verb: "list",
			value: availLabel,
			help:  "Destinations camp discovered, offered when a meeting ends.",
			run:   run(meetingRowPicker),
		},
		{
			name: "Refresh destinations", kind: rowAction, verb: "refresh",
			detail: "camp list + config",
			help:   "Re-run destination discovery.",
			run: func(m *settingsModel) tea.Cmd {
				m.selectMeetingRow(meetingRowRefresh)
				return m.loadRouteDestinations()
			},
		},
	}
}
