package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// A row is a setting. Every section is a list of settingRow values built from
// the live config each time it is drawn. A row that chooses from a list draws
// the whole list under a heading with ✓ on the current item, because seeing
// the options is what a settings screen is for; the other kinds draw as one
// line, name then value.
//
// The kind is the verb:
//
//	rowPick    one of a list — the options are drawn; enter on one chooses it
//	rowChoice  one of a few — drawn on the row with [current] marked; enter,
//	           space or +/- move to the next
//	rowToggle  on or off — enter or space flips it
//	rowSlider  a level — +/- nudge it
//	rowText    typed — enter opens the one-line editor
//	rowAction  does something — enter runs it
//	rowMenu    things to act on — drawn like a list; enter on one acts
//	rowInfo    read-only; enter may explain
type rowKind int

const (
	rowPick rowKind = iota
	rowChoice
	rowToggle
	rowSlider
	rowText
	rowAction
	rowMenu
	rowInfo
)

// When a change takes effect, stated on the row as a fact.
const (
	appliesNow              = "applies immediately"
	appliesNextConversation = "applies to the next conversation"
	appliesOnReturn         = "applies when you return to the conversation"
	appliesNextMeeting      = "applies to the next meeting"
	appliesUnlessPersona    = "applies immediately unless the persona has its own voice"
)

type settingRow struct {
	// key is the config key the row writes; "" for actions and info rows.
	key  string
	name string
	kind rowKind
	// value is the current value as the row shows it; detail is a dim tail.
	value  string
	detail string
	// options and current are the list behind a pick, choice or menu row.
	// current is -1 when nothing in the list is the current value.
	options []string
	current int
	// A disabled row draws dim with its reason and ignores every verb.
	disabled bool
	reason   string
	applies  string
	help     string
	// verb names what enter does on an action or menu row, for the footer.
	verb string
	// muted marks options drawn dim (index-aligned with options; nil means
	// none): a backend that is not available, an engine or tier whose choice
	// starts a download. They are still listed and can still be chosen; the
	// row's help says what choosing one does.
	muted []bool
	// details says what is known about option idx (index-aligned, like
	// muted); while that option is focused it stands in for the help.
	details []string
	// set applies option idx (pick, choice, menu). run performs the row's
	// own verb (toggle, text, action, info). nudge moves a slider by delta
	// steps. preview plays option idx without choosing it. Each takes the
	// model explicitly: a closure built on one copy must not mutate a stale
	// one.
	set     func(m *settingsModel, idx int) tea.Cmd
	run     func(m *settingsModel) tea.Cmd
	nudge   func(m *settingsModel, delta int) tea.Cmd
	preview func(m *settingsModel, idx int) tea.Cmd
}

func onOff(b bool) string {
	if b {
		return "ON"
	}
	return "OFF"
}

func indexOf(options []string, value string) int {
	for i, o := range options {
		if strings.EqualFold(o, value) {
			return i
		}
	}
	return -1
}

// sectionRows is the current section as rows. A section is what you would
// change together: the brain and its model, the voice engine and its voices,
// the microphone and the speaker.
func (m settingsModel) sectionRows() []settingRow {
	switch m.section {
	case sectionBrain:
		rows := append(m.providerRows(), m.modelRows()...)
		rows = append(rows, m.codexRows()...)
		return append(rows, m.decisionRows()...)
	case sectionWorker:
		return m.workerRows()
	case sectionTools:
		return m.toolRows()
	case sectionVoice:
		rows := append(m.ttsRows(), m.voiceFilterRows()...)
		rows = append(rows, m.voiceRows()...)
		return append(rows, m.languageRows()...)
	case sectionQwen:
		return m.qwenRows()
	case sectionSTT:
		return m.sttRows()
	case sectionSpeakers:
		return m.speakerSettingRows()
	case sectionAudio:
		return append(m.inputRows(), m.outputRows()...)
	case sectionMeeting:
		return m.meetingRows()
	}
	return nil
}
