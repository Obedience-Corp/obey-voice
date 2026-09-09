package tui

import (
	"context"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Obedience-Corp/obey-voice/internal/meeting"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

// Meeting settings row indices — keep in lockstep with meetingItems().
const (
	meetingRowDiarization = iota
	meetingRowLiveLabels
	meetingRowDiarizer
	meetingRowNumSpeakers
	meetingRowRecordAudio
	meetingRowMode
	meetingRowDefault
	meetingRowBody
	meetingRowConfigured
	meetingRowPicker
	meetingRowRefresh
	meetingRowCount
)

// meetingNumSpeakerChoices cycles Auto (0) then fixed cluster counts for 1:1
// and small-group meetings. Keep short so Enter cycles quickly.
var meetingNumSpeakerChoices = []int{0, 2, 3, 4}

// meetingItems lists Meeting settings: speaker diarization + notes routing.
// Live conversation speaker analysis is controlled in chat via /speakers, not here.
func (m settingsModel) meetingItems() []string {
	mode := m.cfg.Meeting.Route.Mode
	if mode == "" {
		mode = "ask"
	}
	def := m.cfg.Meeting.Route.Default
	if def == "" {
		def = "(launch campaign)"
	}
	body := m.cfg.Meeting.Route.Body
	if body == "" {
		body = "notes"
	}
	nCfg := len(m.cfg.Meeting.Route.Destinations)
	nAvail := len(m.routeDests)
	var availLabel string
	if m.routeDestsLoading {
		availLabel = "discovering…"
	} else if m.routeDestsErr != "" {
		availLabel = "discovery error"
	} else {
		availLabel = fmt.Sprintf("%d available", nAvail)
	}
	// Effective meeting diarization needs master speaker.enabled + meeting flag.
	diarizationOn := m.cfg.Speaker.Enabled && m.cfg.Speaker.Meeting.Enabled
	liveLabelsOn := m.cfg.Speaker.Enabled && m.cfg.Speaker.Meeting.Live
	recordAudioOn := m.cfg.Speaker.Meeting.RecordAudio
	engine, engineErr := config.MeetingDiarizerFrom(m.cfg)
	return []string{
		fmt.Sprintf("Speaker diarization — %s", enabledLabel(diarizationOn)),
		fmt.Sprintf("Live labels while recording — %s", enabledLabel(liveLabelsOn)),
		m.meetingDiarizerLabel(engine, engineErr),
		fmt.Sprintf("Expected speakers: %s", meetingNumSpeakersLabel(m.cfg.Speaker.Meeting.NumSpeakers, engine)),
		fmt.Sprintf("Record audio for analysis — %s", enabledLabel(recordAudioOn)),
		fmt.Sprintf("Route mode: %s", mode),
		fmt.Sprintf("Default destination: %s", def),
		fmt.Sprintf("Body scope: %s", body),
		fmt.Sprintf("Configured destinations: %d", nCfg),
		fmt.Sprintf("Picker destinations: %s", availLabel),
		"Refresh destinations (camp list + config)",
	}
}

// meetingNumSpeakersLabel describes the count under the engine that would read
// it. Under sortformer it reads nothing — not the model, and not the
// sparse-label merge the count gates under sherpa — so the row says so instead
// of offering tuning advice for a setting that changes nothing.
func meetingNumSpeakersLabel(n int, engine string) string {
	value := "Auto"
	if n > 0 {
		value = fmt.Sprintf("%d", n)
	}
	if engine == config.DiarizerSortformer {
		return value + " — no effect: sortformer takes no speaker count"
	}
	if n <= 0 {
		return "Auto (can over-split long meetings)"
	}
	return fmt.Sprintf("%d (set 2 for 1:1 interviews)", n)
}

// meetingDiarizerLabel is the engine row. A gap replaces the blurb rather than
// extending the line: what is missing matters more than what the engine is.
func (m settingsModel) meetingDiarizerLabel(engine string, err error) string {
	if err != nil {
		return fmt.Sprintf("Diarization engine: unknown value %q — enter picks %s",
			strings.TrimSpace(m.cfg.Speaker.Meeting.Diarizer), config.DefaultMeetingDiarizer)
	}
	if engine != config.DiarizerSortformer {
		return "Diarization engine: " + engine + " (default) — pyannote + TitaNet"
	}
	label := "Diarization engine: sortformer (optional)"
	if gap := m.meetingDiarizerGap(); gap.Missing() {
		return label + " · " + gap.Detail
	}
	return label + " — NVIDIA, through transcribe.cpp"
}

// meetingDiarizerGap is what the selected engine still needs here. The check is
// a PATH lookup and a stat, cheap enough to run while the row renders, so the
// state is current after an install without a Settings reopen.
func (m settingsModel) meetingDiarizerGap() config.STTGap {
	return config.MeetingDiarizerGap(m.cfg, config.ModelsDirFrom(m.cfg), m.binaryLookPath())
}

// nextMeetingDiarizer cycles sherpa → sortformer → sherpa. A value this build
// does not know lands on the default rather than advancing past it: the engine
// that labels a meeting should never be reached by stepping through a typo.
func nextMeetingDiarizer(current string) string {
	choices := config.MeetingDiarizers()
	selected, err := config.NormalizeMeetingDiarizer(current)
	if err != nil {
		return config.DefaultMeetingDiarizer
	}
	for i, choice := range choices {
		if choice == selected {
			return choices[(i+1)%len(choices)]
		}
	}
	return config.DefaultMeetingDiarizer
}

// nextMeetingNumSpeakers cycles Auto → 2 → 3 → 4 → Auto. Values outside the
// list (e.g. hand-edited YAML) enter at the nearest choice then step forward.
func nextMeetingNumSpeakers(current int) int {
	idx := 0
	best := -1
	for i, choice := range meetingNumSpeakerChoices {
		delta := choice - current
		if delta < 0 {
			delta = -delta
		}
		if best < 0 || delta < best {
			best, idx = delta, i
		}
	}
	return meetingNumSpeakerChoices[(idx+1)%len(meetingNumSpeakerChoices)]
}

// setMeetingNumSpeakers saves the expected speaker count (0 = Auto).
func (m *settingsModel) setMeetingNumSpeakers(next int) tea.Cmd {
	if err := m.save("speaker.meeting.num_speakers", next); err != nil {
		m.message = fmt.Sprintf("Failed to save speaker.meeting.num_speakers: %v", err)
		return nil
	}
	m.cfg.Speaker.Meeting.NumSpeakers = next
	engine, _ := config.MeetingDiarizerFrom(m.cfg)
	m.message = fmt.Sprintf("Expected speakers: %s", meetingNumSpeakersLabel(next, engine))
	if engine == config.DiarizerSortformer {
		m.message += fmt.Sprintf(" — it resolves up to %d on its own; set the engine back to %s and the count applies again",
			config.SortformerMaxSpeakers, config.DiarizerSherpa)
	}
	return nil
}

// setRouteMode saves how notes are routed when a meeting ends.
func (m *settingsModel) setRouteMode(next string) tea.Cmd {
	if err := config.SetAndSave("meeting.route.mode", next); err != nil {
		m.message = fmt.Sprintf("Failed to save route mode: %v", err)
		return nil
	}
	m.cfg.Meeting.Route.Mode = next
	m.message = fmt.Sprintf("Meeting route mode: %s", next)
	return nil
}

// setBodyScope saves what the routed body carries: the notes or the full text.
func (m *settingsModel) setBodyScope(next string) tea.Cmd {
	if err := config.SetAndSave("meeting.route.body", next); err != nil {
		m.message = fmt.Sprintf("Failed to save body scope: %v", err)
		return nil
	}
	m.cfg.Meeting.Route.Body = next
	m.message = fmt.Sprintf("Meeting body scope: %s", next)
	return nil
}

// selectMeetingRow performs a Meeting row's own verb, by meetingRow* index.
func (m *settingsModel) selectMeetingRow(row int) {
	saveConfig := m.saveConfig
	if saveConfig == nil {
		saveConfig = config.SetAndSave
	}
	switch row {
	case meetingRowDiarization:
		// Toggle effective meeting diarization: turns master + meeting on together,
		// or meeting off (master left as-is for live /speakers path).
		next := !(m.cfg.Speaker.Enabled && m.cfg.Speaker.Meeting.Enabled)
		if next {
			if err := saveConfig("speaker.enabled", true); err != nil {
				m.message = fmt.Sprintf("Failed to save speaker.enabled: %v", err)
				return
			}
			m.cfg.Speaker.Enabled = true
			if err := saveConfig("speaker.meeting.enabled", true); err != nil {
				m.message = fmt.Sprintf("Failed to save speaker.meeting.enabled: %v", err)
				return
			}
			m.cfg.Speaker.Meeting.Enabled = true
			m.message = "Speaker diarization ON — models install when the next meeting starts"
			return
		}
		if err := saveConfig("speaker.meeting.enabled", false); err != nil {
			m.message = fmt.Sprintf("Failed to save speaker.meeting.enabled: %v", err)
			return
		}
		m.cfg.Speaker.Meeting.Enabled = false
		m.message = "Speaker diarization OFF (live /speakers unchanged)"
	case meetingRowLiveLabels:
		next := !(m.cfg.Speaker.Enabled && m.cfg.Speaker.Meeting.Live)
		if next {
			if !m.cfg.Speaker.Enabled {
				if err := saveConfig("speaker.enabled", true); err != nil {
					m.message = fmt.Sprintf("Failed to save speaker.enabled: %v", err)
					return
				}
				m.cfg.Speaker.Enabled = true
			}
			if err := saveConfig("speaker.meeting.live", true); err != nil {
				m.message = fmt.Sprintf("Failed to save speaker.meeting.live: %v", err)
				return
			}
			m.cfg.Speaker.Meeting.Live = true
			m.message = "Live labels ON — provisional speaker-N while recording (review may refine after stop)"
			return
		}
		if err := saveConfig("speaker.meeting.live", false); err != nil {
			m.message = fmt.Sprintf("Failed to save speaker.meeting.live: %v", err)
			return
		}
		m.cfg.Speaker.Meeting.Live = false
		m.message = "Live labels OFF — mic glyph only until offline diarize"
	case meetingRowDiarizer:
		m.cycleMeetingDiarizer(saveConfig)
	case meetingRowNumSpeakers:
		m.setMeetingNumSpeakers(nextMeetingNumSpeakers(m.cfg.Speaker.Meeting.NumSpeakers))
	case meetingRowRecordAudio:
		next := !m.cfg.Speaker.Meeting.RecordAudio
		if err := saveConfig("speaker.meeting.record_audio", next); err != nil {
			m.message = fmt.Sprintf("Failed to save speaker.meeting.record_audio: %v", err)
			return
		}
		m.cfg.Speaker.Meeting.RecordAudio = next
		m.message = fmt.Sprintf("Record audio for analysis %s", enabledLabel(next))
	case meetingRowMode: // cycle route mode: ask → auto → off → ask
		cur := m.cfg.Meeting.Route.Mode
		if cur == "" {
			cur = "ask"
		}
		next := map[string]string{"ask": "auto", "auto": "off", "off": "ask"}[cur]
		if next == "" {
			next = "ask"
		}
		m.setRouteMode(next)
	case meetingRowDefault:
		m.cycleDefaultDestination()
	case meetingRowBody: // cycle body: notes ↔ full
		next := "full"
		if m.cfg.Meeting.Route.Body == "full" {
			next = "notes"
		}
		m.setBodyScope(next)
	case meetingRowConfigured:
		m.message = "Destinations in " + config.ConfigFile() + " (meeting.route.destinations); campaign default is notes/meetings via import-meeting"
	case meetingRowPicker:
		if m.routeDestsLoading {
			m.message = "Still discovering destinations…"
			return
		}
		if m.routeDestsErr != "" {
			m.message = "Discovery error: " + m.routeDestsErr
			return
		}
		if len(m.routeDests) == 0 {
			m.message = "No destinations yet — install camp or add YAML destinations; start a meeting to pick"
			return
		}
		// Show a short preview of the first few labels.
		n := min(len(m.routeDests), 3)
		parts := make([]string, 0, n)
		for i := 0; i < n; i++ {
			parts = append(parts, meeting.DestinationLabel(m.routeDests[i]))
		}
		more := ""
		if len(m.routeDests) > n {
			more = fmt.Sprintf(" (+%d more)", len(m.routeDests)-n)
		}
		m.message = fmt.Sprintf("%d destination(s): %v%s — choose when starting a meeting", len(m.routeDests), parts, more)
	case meetingRowRefresh:
		m.message = "Refreshing destinations…"
	}
}

// cycleMeetingDiarizer switches the engine behind the offline review pass.
// Nothing else moves: live labels, embeddings and enrolled names stay on the
// sherpa path either way, and a meeting already recording keeps the engine it
// started with.
func (m *settingsModel) cycleMeetingDiarizer(saveConfig func(string, any) error) {
	m.setMeetingDiarizer(nextMeetingDiarizer(m.cfg.Speaker.Meeting.Diarizer))
}

// setMeetingDiarizer saves the engine behind the review pass and says what
// it still needs on this computer.
func (m *settingsModel) setMeetingDiarizer(next string) tea.Cmd {
	// A refusal here would mean this screen's own table disagrees with the
	// schema, which is worth saying rather than writing.
	if _, err := config.NormalizeMeetingDiarizer(next); err != nil {
		m.message = fmt.Sprintf("Not saved: %v", err)
		return nil
	}
	if err := m.save("speaker.meeting.diarizer", next); err != nil {
		m.message = fmt.Sprintf("Failed to save speaker.meeting.diarizer: %v", err)
		return nil
	}
	m.cfg.Speaker.Meeting.Diarizer = next
	if next != config.DiarizerSortformer {
		m.message = "Diarization engine: sherpa — the managed pyannote + TitaNet review pass"
		return nil
	}
	m.message = "Diarization engine: sortformer (optional) — labels the review pass only; live labels stay on sherpa"
	if gap := m.meetingDiarizerGap(); gap.Missing() {
		// The gap already knows whether the blocker is this computer or the
		// review pass being switched off, so the message states it as found.
		m.message += fmt.Sprintf(" · %s — %s", gap.Detail, gap.Remediation)
	}
	return nil
}

// destinationChoice is one entry of the default-destination list: the empty
// id (launch campaign), then configured, then discovered destinations. dest
// is non-nil when choosing it must also persist it.
type destinationChoice struct {
	id   string
	dest *meeting.Destination
}

// defaultDestinationChoices walks empty + configured + discovered IDs.
func (m settingsModel) defaultDestinationChoices() []destinationChoice {
	choices := []destinationChoice{{id: ""}}
	seen := map[string]struct{}{"": {}}
	for _, d := range m.cfg.Meeting.Route.Destinations {
		if d.ID == "" {
			continue
		}
		if _, ok := seen[d.ID]; ok {
			continue
		}
		seen[d.ID] = struct{}{}
		choices = append(choices, destinationChoice{id: d.ID})
	}
	for i := range m.routeDests {
		d := m.routeDests[i]
		if d.ID == "" {
			continue
		}
		if _, ok := seen[d.ID]; ok {
			continue
		}
		seen[d.ID] = struct{}{}
		dd := d
		choices = append(choices, destinationChoice{id: d.ID, dest: &dd})
	}
	return choices
}

// cycleDefaultDestination steps to the next choice.
func (m *settingsModel) cycleDefaultDestination() {
	choices := m.defaultDestinationChoices()
	cur := m.cfg.Meeting.Route.Default
	idx := 0
	for i, c := range choices {
		if c.id == cur {
			idx = i
			break
		}
	}
	m.setDefaultDestination((idx + 1) % len(choices))
}

// setDefaultDestination saves choice idx. When the chosen ID is discovered
// only, it is also persisted into meeting.route.destinations so mode=auto /
// RouteByID can resolve it.
func (m *settingsModel) setDefaultDestination(idx int) tea.Cmd {
	choices := m.defaultDestinationChoices()
	if idx < 0 || idx >= len(choices) {
		return nil
	}
	return m.setDefaultDestinationChoice(choices[idx])
}

// setDefaultDestinationChoice saves one choice — the one a row or picker
// showed, whatever discovery has appended since.
func (m *settingsModel) setDefaultDestinationChoice(next destinationChoice) tea.Cmd {
	// Persist discovered destinations before setting them as default.
	if next.dest != nil {
		if err := m.ensureDestinationConfigured(*next.dest); err != nil {
			m.message = fmt.Sprintf("Failed to save destination: %v", err)
			return nil
		}
	}
	if err := config.SetAndSave("meeting.route.default", next.id); err != nil {
		m.message = fmt.Sprintf("Failed to save default destination: %v", err)
		return nil
	}
	m.cfg.Meeting.Route.Default = next.id
	label := next.id
	if label == "" {
		label = "(launch campaign)"
	}
	m.message = fmt.Sprintf("Default destination: %s", label)
	return nil
}

// ensureDestinationConfigured appends dest to meeting.route.destinations when
// missing so auto/RouteByID can resolve camp-discovered IDs after restart.
func (m *settingsModel) ensureDestinationConfigured(dest meeting.Destination) error {
	for _, d := range m.cfg.Meeting.Route.Destinations {
		if d.ID == dest.ID {
			return nil
		}
	}
	// Persist the normalized capture so restarts do not reintroduce empty or
	// ambiguous values; campaign defaults to import-meeting → notes/meetings.
	norm := meeting.NormalizeDestination(dest)
	entry := config.MeetingDestinationConfig{
		ID:       norm.ID,
		Type:     norm.Type,
		Campaign: norm.Campaign,
		Capture:  norm.Capture,
		Tags:     norm.Tags,
		Path:     norm.Path,
		Folder:   norm.Folder,
	}
	next := append(append([]config.MeetingDestinationConfig{}, m.cfg.Meeting.Route.Destinations...), entry)
	if err := config.SetAndSave("meeting.route.destinations", next); err != nil {
		return err
	}
	m.cfg.Meeting.Route.Destinations = next
	return nil
}

// loadRouteDestinations probes camp list + config for the Meeting settings tab.
func (m *settingsModel) loadRouteDestinations() tea.Cmd {
	m.routeDestsLoading = true
	m.routeDestsErr = ""
	m.routeDestsSeq++
	seq := m.routeDestsSeq
	cfg := m.cfg
	return func() tea.Msg {
		routeCfg := meeting.FromConfig(cfg)
		router := meeting.NewDefaultRouter(routeCfg)
		ctx, cancel := context.WithTimeout(context.Background(), meeting.DiscoverTimeout)
		defer cancel()
		dests, err := router.DiscoverDestinations(ctx)
		return meetingRouteDestsMsg{seq: seq, dests: dests, err: err}
	}
}

// meetingRouteDestsMsg is async discovery for Settings → Meeting.
type meetingRouteDestsMsg struct {
	seq   int
	dests []meeting.Destination
	err   error
}
