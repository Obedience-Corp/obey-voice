package tui

import (
	"context"
	"fmt"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio/playback"
	ttsproviders "github.com/Obedience-Corp/obey-voice/pkg/voiceagent/tts/providers"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/Obedience-Corp/obey-voice/internal/discovery"
	"github.com/Obedience-Corp/obey-voice/internal/persona"
	"github.com/Obedience-Corp/obey-voice/internal/persona/campdirs"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

const personasCreateLabel = "+ Create new persona…"

// personasModel is the main-menu screen for listing, switching, creating, and
// editing voice agent personas (including system prompts).
type personasModel struct {
	audioDraft       personaAudioDraft
	audioSetupCancel context.CancelFunc
	audioSetupSeq    uint64
	// returnScreen keeps Esc/q tied to the screen that opened the manager.
	returnScreen screen
	cfg          *config.Config
	width        int
	height       int
	cursor       int
	offset       int

	items        []*persona.Profile
	loadErr      string
	loadWarning  string
	stacks       map[string]personaListStack
	defaultsErr  string
	listDefaults *config.Config
	loadDefaults func() (*config.Config, error)
	message      string

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
	workerOllamaHost  string
	// Model row picker: discovered models per provider + async scan state.
	// See personas_models.go; brainModelInput stays the selected value.
	brainModels personaModelPicker
	// Starting directory row: the folder new conversations with this persona
	// run file & shell tools in. directoryInput holds the path; the picker
	// offers registered camps and a (custom) field. See personas_directory.go.
	directoryInput textinput.Model
	directory      personaDirectoryPicker
	// Voice row catalog for providers that have to be asked (openai-speech),
	// and the Language row's filter over whichever catalog is showing.
	// See personas_voices.go.
	voices      voiceCatalog
	voiceLocale string

	preview voicePreview

	listPersonas         func() ([]*persona.Profile, error)
	usePersona           func(*config.Config, string) error
	createPersona        func(*config.Config, persona.CreateOpts) (*persona.Profile, error)
	savePrompt           func(id, systemPrompt string) (*persona.Profile, error)
	saveName             func(id, displayName string) (*persona.Profile, error)
	saveStack            func(id string, b persona.Brain, t persona.TTS) (*persona.Profile, error)
	saveDirectory        func(id, directory string) (*persona.Profile, error)
	deletePersona        func(*config.Config, string) (*persona.DeleteResult, error)
	setInactive          func(*config.Config, string, bool) (*persona.Profile, error)
	selectedPersona      func() string
	loadPrompt           func(name string) (string, error)
	loadPromptForProfile func(*persona.Profile) (string, error)
	defaultPrompt        func() (string, error)
	starterPrompt        func() (string, error)

	// In a conversation the manager submits to the live session owner. The
	// launcher keeps usePersona below, which is the persistent default path.
	requestSelection func(string) tea.Cmd
	// startPersona is used only when a failed conversation startup returns to
	// the chooser. It starts a fresh build for the chosen existing profile and
	// does not mutate the global active-persona preference.
	startPersona func(string, string) tea.Cmd
}

func newPersonas(cfg *config.Config) personasModel {
	m := personasModel{
		cfg:           cfg,
		loadDefaults:  config.LoadRaw,
		usePersona:    persona.Use,
		createPersona: persona.CreateAndUseWithOpts,
		savePrompt:    persona.UpdateSystemPrompt,
		saveName:      persona.UpdateDisplayName,
		saveStack:     persona.UpdateStack,
		saveDirectory: persona.UpdateStartingDirectory,
		deletePersona: persona.Delete,
		setInactive:   persona.SetInactive,
		loadPrompt:    persona.LoadSystemPrompt,
		// Prefer profile-aware load so a private prompts/persona/<id>.yaml wins
		// over a stale prompts.persona ref (e.g. TTS voice name as the ref).
		loadPromptForProfile: persona.LoadSystemPromptForProfile,
		defaultPrompt:        persona.DefaultSystemPrompt,
		starterPrompt:        persona.StarterSystemPrompt,
		nameInput:            newPersonaCreateInput(),
		promptTA:             newPromptEditor(),
		brainModelInput:      newPersonaStackInput("empty = provider default"),
		directoryInput:       newPersonaDirectoryInput(),
		preview: voicePreview{
			newPreviewPlayer: func() audio.Engine {
				if cfg == nil {
					return playback.NewPlayer()
				}
				player := playback.NewPlayerWithDevice(cfg.OutputDevice)
				player.SetVolume(config.PlaybackVolumeOf(cfg))
				return player
			},
			ensure: func(ctx context.Context, c *config.Config, onProgress func(string, float64)) error {
				return config.EnsureRuntimeAssets(ctx, c, config.AssetRequest{NeedTTS: true}, onProgress)
			},
			newTTS: ttsproviders.NewProvider,
		},
	}
	// The Model row picks from the same discovery Settings uses. It runs as a
	// command on form open, never here: constructing the screen must not block
	// on an ollama socket (and tests must not reach the network at all).
	m.brainModels.discover = discovery.DiscoverProvidersContext
	// The Directory row lists registered camps the same way Settings → Meeting
	// finds route destinations: `camp list --json`, on form open, never here.
	m.directory.discover = campdirs.DiscoverDefault
	m.voices.load = liveVoiceCatalog
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

func (m *personasModel) reload() {
	m.loadErr, m.loadWarning = "", ""
	var items []*persona.Profile
	var err error
	if m.listPersonas != nil {
		items, err = m.listPersonas()
	} else {
		var issues []persona.CatalogIssue
		items, issues, err = persona.ListWithIssues()
		if len(issues) > 0 {
			ids := make([]string, 0, len(issues))
			for _, issue := range issues {
				ids = append(ids, issue.ID)
			}
			m.loadWarning = "Unavailable: " + strings.Join(ids, ", ") + "; repair persona.yaml."
		}
	}
	if err != nil {
		m.loadErr = err.Error()
		m.items = nil
		return
	}
	m.items = items
	m.reloadStacks()
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
	rows := m.listBodyRows() - 1 // column headings
	if detail := m.personaDetailLines(); len(detail) > 0 {
		rows -= len(detail) + 1
	}
	return max(rows/m.personaRowHeight(), 1)
}

func (m personasModel) Update(msg tea.Msg) (personasModel, tea.Cmd) {
	switch msg := msg.(type) {
	case personaAudioSetupMsg:
		m.receiveAudioSetup(msg)
		return m, nil
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.resizeForm()
		m.ensureVisible()

	case personaModelsMsg:
		m.applyBrainModels(msg)

	case personaDirectoriesMsg:
		m.applyDirectories(msg)

	case personaVoicesMsg:
		m.applyVoiceCatalog(msg)

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
		// A sample belongs to the highlighted row, not the next screen or row.
		switch msg.String() {
		case "up", "k", "down", "j", "enter", "n", "c", "e", "m", "d", "delete", "x", "esc", "q":
			if m.preview.previewing != "" {
				m.preview.cancel()
				m.message = "Preview stopped"
			}
		}
		switch msg.String() {
		case "p":
			return m, m.previewSelectedPersona()
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
		case "x":
			m.toggleInactive()
		case "esc", "q":
			return m, func() tea.Msg { return switchScreenMsg(m.returnScreen) }
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
	if p.Inactive {
		m.message = fmt.Sprintf("%s is inactive; press x to enable it", displayName(p))
		return nil
	}
	if m.startPersona != nil {
		name := displayName(p)
		m.message = fmt.Sprintf("Starting conversation with %s…", name)
		start := m.startPersona
		return start(p.ID, name)
	}
	if m.returnScreen == screenConversation {
		if m.requestSelection == nil {
			m.message = "Persona switching is unavailable for this conversation"
			return nil
		}
		m.message = fmt.Sprintf("Queued persona: %s", displayName(p))
		return m.requestSelection(p.ID)
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
	name := displayName(p)
	m.message = fmt.Sprintf("Active persona: %s · press e to edit name/prompt", name)
	return nil
}

func displayName(p *persona.Profile) string {
	if p == nil {
		return ""
	}
	if p.DisplayName != "" {
		return p.DisplayName
	}
	return p.ID
}
