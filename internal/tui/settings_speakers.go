package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Obedience-Corp/obey-voice/internal/speaker"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

// Speakers row kinds. The first speakerControlRowCount kinds are the fixed
// control rows (indices 0–6). Enroll-another and guest rows are appended
// after that prefix; their kind is not an index.
const (
	speakerRowName = iota
	speakerRowLive
	speakerRowThreshold
	speakerRowWindow
	speakerRowMeeting
	speakerRowRegister
	speakerRowRemove
	speakerControlRowCount
	speakerRowEnrollOther
	speakerRowGuest
)

type speakerRow struct {
	kind  int
	name  string // guest profile name when kind == speakerRowGuest
	label string
}

// liveThresholdChoices are the match cutoffs offered for chat labels. Lower
// merges similar voices onto one label; higher splits a voice into a fresh
// speaker-N more readily. Values stay inside the band searchThreshold accepts,
// so every choice reaches the engine unclamped.
var liveThresholdChoices = []float32{0.45, 0.55, 0.6, 0.7}

// liveWindowChoices are analysis window lengths in ms. Shorter labels a turn
// sooner; longer gives the embedder more voice to work with. All are above the
// 0.5s floor StartLiveFeed enforces.
var liveWindowChoices = []int{1000, 1500, 2000, 3000}

// speakerItems lists live conversation speaker analysis — the path that labels
// chat bubbles. Meeting diarization is shown read-only because it is an offline
// per-meeting concern owned by the Meeting tab; surfacing its state here means
// someone looking for "speakers" sees both answers in one place instead of
// concluding the chat toggle is missing.
func (m settingsModel) speakerItems() []string {
	rows := m.speakerRows()
	items := make([]string, len(rows))
	for i, row := range rows {
		items[i] = row.label
	}
	return items
}

func (m settingsModel) speakerRows() []speakerRow {
	sp := m.cfg.Speaker
	liveOn := sp.Enabled && sp.Live.Enabled
	meetingOn := sp.Enabled && sp.Meeting.Enabled

	threshold := sp.Live.Threshold
	if threshold <= 0 {
		threshold = sp.Threshold
	}
	if threshold <= 0 {
		threshold = 0.6
	}
	window := sp.Live.WindowMS
	if window <= 0 {
		window = 1500
	}

	name := strings.TrimSpace(m.cfg.UserDisplayName)
	if name == "" {
		name = "not set"
	}

	rows := []speakerRow{
		{kind: speakerRowName, label: fmt.Sprintf("Your name — %s", name)},
		{kind: speakerRowLive, label: fmt.Sprintf("Speaker labels in chat — %s", enabledLabel(liveOn))},
		{kind: speakerRowThreshold, label: fmt.Sprintf("Match threshold: %.2f", threshold)},
		{kind: speakerRowWindow, label: fmt.Sprintf("Analysis window: %d ms", window)},
		{kind: speakerRowMeeting, label: fmt.Sprintf("Meeting diarization — %s (set in the Meeting tab)", enabledLabel(meetingOn))},
		{kind: speakerRowRegister, label: m.registerRowLabel()},
		{kind: speakerRowRemove, label: m.removeRowLabel()},
	}
	if len(m.enrolledProfiles) == 0 {
		return rows
	}
	rows = append(rows, speakerRow{kind: speakerRowEnrollOther, label: "Enroll another speaker…"})
	expected := speaker.ExpectedLiveRev(speaker.FromAppConfig(m.cfg))
	for _, p := range m.enrolledProfiles {
		if p.Owner {
			continue
		}
		label := p.Name
		if p.ModelRev != expected {
			label += " (re-enroll to refresh)"
		}
		rows = append(rows, speakerRow{kind: speakerRowGuest, name: p.Name, label: label})
	}
	return rows
}

func (m settingsModel) onGuestRow() bool {
	rows := m.speakerRows()
	if m.cursor < 0 || m.cursor >= len(rows) {
		return false
	}
	return rows[m.cursor].kind == speakerRowGuest
}

func (m settingsModel) isOwnerName(name string) bool {
	return m.enrolledOwnerOK && strings.EqualFold(m.enrolledOwner.Name, strings.TrimSpace(name))
}

// registerRowLabel names the enrollment action for the state the store is in.
// A stale owner keeps the same wording `speaker list` uses, because it is the
// same problem: those embeddings no longer match what live analysis produces.
func (m settingsModel) registerRowLabel() string {
	if !m.enrolledOwnerOK {
		return "Register your voice"
	}
	label := fmt.Sprintf("Re-enroll your voice — %s", m.enrolledOwner.Name)
	if m.ownerRevIsStale() {
		label += " (re-enroll to refresh)"
	}
	return label
}

func (m settingsModel) removeRowLabel() string {
	if !m.enrolledOwnerOK {
		return "Remove your voice — (nothing registered)"
	}
	return fmt.Sprintf("Remove your voice — %s", m.enrolledOwner.Name)
}

func (m *settingsModel) selectSpeakerItem() tea.Cmd {
	rows := m.speakerRows()
	if m.cursor < 0 || m.cursor >= len(rows) {
		return nil
	}
	row := rows[m.cursor]
	saveConfig := m.saveConfig
	if saveConfig == nil {
		saveConfig = config.SetAndSave
	}
	switch row.kind {
	case speakerRowName:
		return m.beginEdit(editUserDisplayName, "Your name", strings.TrimSpace(m.cfg.UserDisplayName))
	case speakerRowLive:
		m.toggleLiveSpeakers(saveConfig)
	case speakerRowThreshold:
		next := nextLiveThreshold(m.effectiveLiveThreshold())
		if err := saveConfig("speaker.live.threshold", next); err != nil {
			m.message = fmt.Sprintf("Failed to save live threshold: %v", err)
			return nil
		}
		m.cfg.Speaker.Live.Threshold = next
		m.message = fmt.Sprintf("Match threshold %.2f — lower merges similar voices, higher splits them", next)
	case speakerRowWindow:
		next := nextLiveWindow(m.cfg.Speaker.Live.WindowMS)
		if err := saveConfig("speaker.live.window_ms", next); err != nil {
			m.message = fmt.Sprintf("Failed to save live window: %v", err)
			return nil
		}
		m.cfg.Speaker.Live.WindowMS = next
		m.message = fmt.Sprintf("Analysis window %d ms", next)
		if len(m.enrolledProfiles) > 0 {
			m.message += " — enrolled voices go stale until you re-enroll"
		}
	case speakerRowMeeting:
		m.message = "Meeting diarization is per-meeting — toggle it in Settings → Meeting"
	case speakerRowRegister:
		return m.beginEnroll()
	case speakerRowRemove:
		m.beginRemoveVoice()
	case speakerRowEnrollOther:
		return m.beginEdit(editSpeakerEnrollName, "Speaker name", "")
	case speakerRowGuest:
		return m.startEnroll(row.name, false)
	}
	return nil
}

// commitUserDisplayName saves the typed name. It returns a Cmd because the
// editor is also the first step of a registration that had no name to use:
// that flow resumes here, once the name is really saved and non-empty.
func (m *settingsModel) commitUserDisplayName(value string) tea.Cmd {
	resume := m.enrollAfterName
	m.enrollAfterName = false

	saveConfig := m.saveConfig
	if saveConfig == nil {
		saveConfig = config.SetAndSave
	}
	value = strings.TrimSpace(value)
	if err := saveConfig("user_display_name", value); err != nil {
		m.message = fmt.Sprintf("Failed to save your name: %v", err)
		return nil
	}
	m.cfg.UserDisplayName = value
	if value == "" {
		m.message = "Your name cleared — new conversations show speaker-1 until you enroll or /speakers name"
		if resume {
			m.message = "Registration needs a name — nothing was recorded"
		}
		return nil
	}
	m.message = fmt.Sprintf("Your name is %s — new conversations use it in place of speaker-1", value)
	if resume {
		return m.startEnroll(value, true)
	}
	return nil
}

// toggleLiveSpeakers flips chat labels. Turning on also turns on the master
// speaker switch, since live analysis is gated behind both; turning off leaves
// the master alone so meeting diarization keeps working.
func (m *settingsModel) toggleLiveSpeakers(saveConfig func(string, any) error) {
	sp := m.cfg.Speaker
	if sp.Enabled && sp.Live.Enabled {
		if err := saveConfig("speaker.live.enabled", false); err != nil {
			m.message = fmt.Sprintf("Failed to save speaker.live.enabled: %v", err)
			return
		}
		m.cfg.Speaker.Live.Enabled = false
		m.message = "Speaker labels in chat OFF (meeting diarization unchanged)"
		return
	}
	if !sp.Enabled {
		if err := saveConfig("speaker.enabled", true); err != nil {
			m.message = fmt.Sprintf("Failed to save speaker.enabled: %v", err)
			return
		}
		m.cfg.Speaker.Enabled = true
	}
	if err := saveConfig("speaker.live.enabled", true); err != nil {
		m.message = fmt.Sprintf("Failed to save speaker.live.enabled: %v", err)
		return
	}
	m.cfg.Speaker.Live.Enabled = true
	m.message = "Speaker labels in chat ON — new conversations label turns; in an open chat run /speakers on"
}

// effectiveLiveThreshold resolves the cutoff the engine would actually use,
// mirroring speaker.Config.Normalize's inherit-from-root rule.
func (m settingsModel) effectiveLiveThreshold() float32 {
	if m.cfg.Speaker.Live.Threshold > 0 {
		return m.cfg.Speaker.Live.Threshold
	}
	if m.cfg.Speaker.Threshold > 0 {
		return m.cfg.Speaker.Threshold
	}
	return 0.6
}

// nextLiveThreshold cycles to the next offered cutoff, entering the list at the
// closest value so an out-of-band config lands somewhere sensible.
func nextLiveThreshold(current float32) float32 {
	idx := 0
	best := float32(-1)
	for i, choice := range liveThresholdChoices {
		delta := choice - current
		if delta < 0 {
			delta = -delta
		}
		if best < 0 || delta < best {
			best, idx = delta, i
		}
	}
	return liveThresholdChoices[(idx+1)%len(liveThresholdChoices)]
}

// nextLiveWindow cycles window lengths, treating unset (0) as the default.
func nextLiveWindow(current int) int {
	if current <= 0 {
		current = 1500
	}
	for i, choice := range liveWindowChoices {
		if choice == current {
			return liveWindowChoices[(i+1)%len(liveWindowChoices)]
		}
	}
	return liveWindowChoices[0]
}
