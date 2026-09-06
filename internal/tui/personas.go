package tui

import (
	"context"
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/Obedience-Corp/obey-voice/internal/discovery"
	"github.com/Obedience-Corp/obey-voice/internal/persona"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/tts"
)

const personasCreateLabel = "+ Create new persona…"

// personasModel is the main-menu screen for listing, switching, creating, and
// editing voice agent personas (including system prompts).
type personasModel struct {
	cfg    *config.Config
	width  int
	height int
	cursor int
	offset int

	items   []*persona.Profile
	loadErr string
	message string

	// formMode: "" | "create" | "edit"
	formMode string
	// formRow is the cursor over the one-screen form: name, the model & voice
	// rows, then the system prompt. promptOpen hands the whole screen to the
	// prompt editor — the one field that needs it.
	formRow    int
	promptOpen bool
	editID     string
	nameInput  textinput.Model
	promptTA   promptEditor
	// formInitialPrompt is the prompt body the form opened with; esc-to-cancel
	// on the name step is allowed only while the prompt still matches it.
	formInitialPrompt string

	// confirmDeleteID is the persona a delete is pending on; "" when nothing
	// is awaiting an answer. Deletion is not reversible, so it never happens
	// on the keystroke that asks for it.
	confirmDeleteID   string
	confirmDeleteName string

	// Model & voice rows: per-persona brain provider/model + TTS provider/voice.
	// Index 0 of the provider lists is "(default)" = inherit the app config.
	// Voice is a selectable catalog (←/→), not free text — empty selectedVoice
	// means inherit the Settings default for the effective TTS provider.
	brainProviderIdx int
	ttsProviderIdx   int
	brainModelInput  textinput.Model
	selectedVoice    string
	selectedTier     string
	// Worker rows: the brain that runs this persona's spawned background jobs.
	// Chosen here rather than inherited silently — a persona that talks locally
	// and works on a remote CLI is a decision, not a default.
	workerProviderIdx int
	workerModel       string
	// Model row picker: discovered models per provider + async scan state.
	// See personas_models.go; brainModelInput stays the selected value.
	brainModels personaModelPicker

	preview voicePreview

	listPersonas         func() ([]*persona.Profile, error)
	usePersona           func(*config.Config, string) error
	createPersona        func(*config.Config, persona.CreateOpts) (*persona.Profile, error)
	savePrompt           func(id, systemPrompt string) (*persona.Profile, error)
	saveName             func(id, displayName string) (*persona.Profile, error)
	saveStack            func(id string, b persona.Brain, t persona.TTS) (*persona.Profile, error)
	deletePersona        func(*config.Config, string) (*persona.DeleteResult, error)
	loadPrompt           func(name string) (string, error)
	loadPromptForProfile func(*persona.Profile) (string, error)
	defaultPrompt        func() (string, error)
	starterPrompt        func() (string, error)
}

func newPersonas(cfg *config.Config) personasModel {
	m := personasModel{
		cfg:           cfg,
		listPersonas:  persona.List,
		usePersona:    persona.Use,
		createPersona: persona.CreateAndUseWithOpts,
		savePrompt:    persona.UpdateSystemPrompt,
		saveName:      persona.UpdateDisplayName,
		saveStack:     persona.UpdateStack,
		deletePersona: persona.Delete,
		loadPrompt:    persona.LoadSystemPrompt,
		// Prefer profile-aware load so a private prompts/persona/<id>.yaml wins
		// over a stale prompts.persona ref (e.g. TTS voice name as the ref).
		loadPromptForProfile: persona.LoadSystemPromptForProfile,
		defaultPrompt:        persona.DefaultSystemPrompt,
		starterPrompt:        persona.StarterSystemPrompt,
		nameInput:            newPersonaCreateInput(),
		promptTA:             newPromptEditor(),
		brainModelInput:      newPersonaStackInput("empty = provider default"),
		preview: voicePreview{
			newPreviewPlayer: func() audio.Engine {
				if cfg == nil {
					return audio.NewPlayer()
				}
				return audio.NewPlayerWithDevice(cfg.OutputDevice)
			},
			ensure: func(ctx context.Context, c *config.Config, onProgress func(string, float64)) error {
				return config.EnsureRuntimeAssets(ctx, c, config.AssetRequest{NeedTTS: true}, onProgress)
			},
			newTTS: tts.NewProvider,
		},
	}
	// The Model row picks from the same discovery Settings uses. It runs as a
	// command on form open, never here: constructing the screen must not block
	// on an ollama socket (and tests must not reach the network at all).
	m.brainModels.discover = discovery.DiscoverProvidersContext
	m.reload()
	return m
}

// newPersonaCreateInput builds the name field for the create/edit form. The
// label lives on the field box, so the input itself carries no prompt text.
func newPersonaCreateInput() textinput.Model {
	ti := textinput.New()
	ti.Placeholder = "Research buddy"
	ti.CharLimit = 64
	ti.Width = 40
	ti.Prompt = ""
	return ti
}

// personaListLabel formats one row for the Personas list.
func personaListLabel(p *persona.Profile) string {
	if p == nil {
		return ""
	}
	label := p.DisplayName
	if label == "" {
		label = p.ID
	}
	if p.ID != "" && p.DisplayName != "" && !strings.EqualFold(p.DisplayName, p.ID) {
		label = fmt.Sprintf("%s (%s)", p.DisplayName, p.ID)
	}
	detail := strings.TrimSpace(p.TTS.Provider)
	if v := strings.TrimSpace(p.TTS.Voice); v != "" {
		if detail != "" {
			detail += " · " + v
		} else {
			detail = v
		}
	}
	// The built-in default is worth naming in the list: it pins nothing, so
	// without this its row is the only one with no voice to show for itself.
	// Both halves matter — the id alone would badge a user's own persona that
	// happens to be named obey as shipped content.
	if p.Builtin && p.ID == persona.DefaultID {
		if detail != "" {
			detail = "built-in default · " + detail
		} else {
			detail = "built-in default"
		}
	}
	if detail != "" {
		label += " — " + detail
	}
	return label
}

func (m *personasModel) reload() {
	m.loadErr = ""
	list := m.listPersonas
	if list == nil {
		list = persona.List
	}
	items, err := list()
	if err != nil {
		m.loadErr = err.Error()
		m.items = nil
		return
	}
	m.items = items
}

func (m personasModel) listLen() int {
	if m.loadErr != "" {
		return 1
	}
	return len(m.items) + 1 // trailing create row
}

func (m *personasModel) ensureVisible() {
	visible := m.visibleRows()
	if m.cursor < m.offset {
		m.offset = m.cursor
	}
	if m.cursor >= m.offset+visible {
		m.offset = m.cursor - visible + 1
	}
	maxOffset := max(m.listLen()-visible, 0)
	m.offset = min(max(m.offset, 0), maxOffset)
}

func (m personasModel) visibleRows() int {
	h := m.height
	if h <= 0 {
		h = 24
	}
	if h < 10 {
		return max(h-2, 1)
	}
	return max(h-6, 1)
}

func (m personasModel) Update(msg tea.Msg) (personasModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.resizeForm()
		m.ensureVisible()

	case personaModelsMsg:
		m.applyBrainModels(msg)

	case voicePreviewDoneMsg:
		if m.preview.applyDone(msg) && msg.message != "" {
			m.message = msg.message
		}

	case tea.KeyMsg:
		if m.formMode != "" {
			return m.updateForm(msg)
		}
		if m.confirmDeleteID != "" {
			return m.updateDeleteConfirm(msg)
		}
		switch msg.String() {
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			if m.cursor < m.listLen()-1 {
				m.cursor++
			}
		case "enter":
			return m, m.selectCurrent()
		case "n", "c":
			// Create without scrolling to the trailing "+ Create" row.
			return m, m.beginCreate()
		case "e":
			return m, m.beginEditAt(formRowName)
		case "m":
			// The most-changed fields, one key from the list.
			return m, m.beginEditAt(formRowBrainProvider)
		case "d", "delete":
			m.beginDelete()
		case "esc", "q":
			return m, func() tea.Msg { return switchScreenMsg(screenLauncher) }
		}
		m.ensureVisible()

	}
	return m, nil
}

func (m *personasModel) selectCurrent() tea.Cmd {
	if m.loadErr != "" {
		return nil
	}
	if len(m.items) == 0 || m.cursor == len(m.items) {
		return m.beginCreate()
	}
	if m.cursor < 0 || m.cursor >= len(m.items) {
		return nil
	}
	p := m.items[m.cursor]
	if p == nil {
		return nil
	}
	use := m.usePersona
	if use == nil {
		use = persona.Use
	}
	if err := use(m.cfg, p.ID); err != nil {
		m.message = fmt.Sprintf("Failed to switch persona: %v", err)
		return nil
	}
	m.reload()
	name := p.DisplayName
	if name == "" {
		name = p.ID
	}
	m.message = fmt.Sprintf("Active persona: %s · press e to edit name/prompt", name)
	return nil
}
