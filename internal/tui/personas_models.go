package tui

import (
	"context"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	ansi "github.com/charmbracelet/x/ansi"

	"github.com/Obedience-Corp/obey-voice/internal/discovery"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

// stackCustomLabel is the escape hatch on the Model row: it reveals the free
// text field so a model the scan did not list (a tag not pulled yet, a remote
// name) is still reachable.
const stackCustomLabel = "(custom)"

// personaModelsTimeout caps a whole form-open scan. discovery probes each
// provider with its own 3s budget, so the ceiling covers a slow grok CLI plus a
// slow ollama socket without leaving the row spinning.
const personaModelsTimeout = 8 * time.Second

// personaModelPicker holds the Model row's discovered catalog and scan state.
// The selected model itself lives in personasModel.brainModelInput — that field
// stays the single source of truth so the picker and the free text field can
// never disagree; `custom` only says which of the two owns the row.
type personaModelPicker struct {
	// discover is the injection seam. Tests replace it so the form never
	// touches the network; production wires discovery.DiscoverProvidersContext.
	discover func(context.Context, *config.Config) []discovery.ProviderInfo

	// seq invalidates in-flight scans (form reopened, provider rescanned).
	seq     int
	loading bool
	loaded  bool
	err     string
	custom  bool
	// customText remembers what was typed under "(custom)" so cycling off the
	// entry and back inside one form-open does not destroy it. Cleared when the
	// form closes, never mid-form.
	customText string
	// byProvider is lower-cased provider name → selectable models.
	byProvider map[string][]string
	cancel     context.CancelFunc
}

// personaModelsMsg carries an async provider scan back to the form.
type personaModelsMsg struct {
	seq       int
	providers []discovery.ProviderInfo
	err       error
}

// loadBrainModels starts a provider scan for the open form. It never blocks the
// form: the row renders "(default)" + "(custom)" with a scanning note until the
// message lands. Cached per form-open — keystrokes never re-fetch.
func (m *personasModel) loadBrainModels() tea.Cmd {
	discover := m.brainModels.discover
	if discover == nil {
		return nil
	}
	m.cancelBrainModels()
	m.brainModels.seq++
	m.brainModels.loading = true
	m.brainModels.loaded = false
	m.brainModels.err = ""
	m.brainModels.byProvider = nil

	seq := m.brainModels.seq
	cfg := m.cfg
	ctx, cancel := context.WithCancel(context.Background())
	m.brainModels.cancel = cancel

	return func() tea.Msg {
		scanCtx, scanCancel := context.WithTimeout(ctx, personaModelsTimeout)
		defer scanCancel()
		if err := scanCtx.Err(); err != nil {
			return personaModelsMsg{seq: seq, err: err}
		}
		infos := discover(scanCtx, cfg)
		return personaModelsMsg{seq: seq, providers: infos, err: scanCtx.Err()}
	}
}

// cancelBrainModels stops an in-flight scan (form closed, or superseded).
func (m *personasModel) cancelBrainModels() {
	if m.brainModels.cancel != nil {
		m.brainModels.cancel()
		m.brainModels.cancel = nil
	}
}

// resetBrainModels drops scan state when the form closes, keeping the injected
// discover seam. The seq bump makes any in-flight result stale on arrival.
func (m *personasModel) resetBrainModels() {
	m.cancelBrainModels()
	m.brainModels.seq++
	m.brainModels.loading = false
	m.brainModels.loaded = false
	m.brainModels.err = ""
	m.brainModels.byProvider = nil
	m.brainModels.clearCustom()
}

// clearCustom drops the "(custom)" mode and the text typed under it. Only a
// closing (or reopening) form may do this — cycling must not.
func (p *personaModelPicker) clearCustom() {
	p.custom = false
	p.customText = ""
}

// applyBrainModels folds a scan result into the row, dropping stale results.
func (m *personasModel) applyBrainModels(msg personaModelsMsg) {
	if msg.seq != m.brainModels.seq {
		return
	}
	m.cancelBrainModels()
	m.brainModels.loading = false
	if msg.err != nil {
		// A cancelled or timed-out scan is not a catalog: keep the row on
		// "(default)"/"(custom)" and say so rather than claiming zero models.
		m.brainModels.err = msg.err.Error()
		return
	}
	byProvider := make(map[string][]string, len(msg.providers))
	for _, p := range msg.providers {
		byProvider[strings.ToLower(strings.TrimSpace(p.Name))] = selectableModels(p.Models)
	}
	m.brainModels.byProvider = byProvider
	m.brainModels.loaded = true
	m.brainModels.err = ""
	// Canonicalize the prefilled model against the freshly discovered catalog
	// (ollama reports "llama3" for "llama3:latest").
	m.canonicalizeBrainModel()
}

// selectableModels drops entries that are not really pickable: the literal
// "default" both claude and a degraded grok probe report is what "(default)"
// already means, so listing it twice would be a lie about what claude offers.
func selectableModels(models []string) []string {
	out := make([]string, 0, len(models))
	seen := map[string]bool{}
	for _, name := range models {
		name = strings.TrimSpace(name)
		if name == "" || strings.EqualFold(name, "default") {
			continue
		}
		key := strings.ToLower(name)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, name)
	}
	return out
}

// effectiveStackBrainProvider is the provider the Model row describes: the form
// selection, or the app Settings provider when the row is "(default)".
func (m personasModel) effectiveStackBrainProvider() string {
	provider := providerAt(stackBrainProviders(), m.brainProviderIdx)
	if provider == "" && m.cfg != nil {
		provider = strings.TrimSpace(m.cfg.BrainProvider)
	}
	return provider
}

// brainModelCatalog returns the discovered models for a provider. ok is false
// while the scan is pending or failed — callers must not treat "unknown" as
// "empty" (that would clamp away a perfectly good saved model).
func (m personasModel) brainModelCatalog(provider string) (models []string, ok bool) {
	if !m.brainModels.loaded {
		return nil, false
	}
	list, found := m.brainModels.byProvider[strings.ToLower(strings.TrimSpace(provider))]
	return list, found
}

// stackModelValue is the model recorded on the persona ("" = inherit default).
func (m personasModel) stackModelValue() string {
	return strings.TrimSpace(m.brainModelInput.Value())
}

// stackModelList builds the Model row: (default) + the discovered models for
// the effective brain provider, plus any current value the scan did not list
// (so edit never drops a hand-written model), and (custom) last.
func (m personasModel) stackModelList() []string {
	out := []string{stackDefaultLabel}
	models, _ := m.brainModelCatalog(m.effectiveStackBrainProvider())
	out = append(out, models...)
	if v := m.stackModelValue(); v != "" {
		found := false
		for _, item := range out[1:] {
			if strings.EqualFold(item, v) {
				found = true
				break
			}
		}
		if !found {
			out = append(out, v)
		}
	}
	return append(out, stackCustomLabel)
}

// stackModelIndex maps the current value onto stackModelList.
func (m personasModel) stackModelIndex() int {
	list := m.stackModelList()
	if m.brainModels.custom {
		return len(list) - 1
	}
	v := m.stackModelValue()
	if v == "" {
		return 0
	}
	for i, item := range list {
		if i == 0 {
			continue
		}
		if strings.EqualFold(item, v) {
			return i
		}
	}
	return 0
}

// cycleStackModel moves ←/→ through the model catalog for the current brain
// provider. Landing on (custom) focuses the free text field; leaving it blurs.
// Both directions out of (custom) are non-destructive: the typed text is kept
// and restored on the way back, so wrapping past "(default)" costs nothing.
func (m *personasModel) cycleStackModel(delta int) {
	list := m.stackModelList()
	idx := (m.stackModelIndex() + delta + len(list)) % len(list)
	custom := idx == len(list)-1
	if m.brainModels.custom && !custom {
		m.brainModels.customText = m.stackModelValue()
	}
	switch {
	case custom:
		m.brainModels.custom = true
		if text := m.brainModels.customText; text != "" {
			m.brainModelInput.SetValue(text)
		}
		m.brainModelInput.Focus()
		m.brainModelInput.CursorEnd()
	case idx == 0:
		m.brainModels.custom = false
		m.brainModelInput.SetValue("")
		m.brainModelInput.Blur()
	default:
		m.brainModels.custom = false
		m.brainModelInput.SetValue(list[idx])
		m.brainModelInput.Blur()
	}
	m.message = m.formRowHelp()
}

// formRowHelp says what the row under the cursor means. The footer already
// lists the keys, so this line spends itself on the field instead — and the
// (custom) typing hint is scoped to the row that owns it, never left behind.
func (m personasModel) formRowHelp() string {
	switch m.formRow {
	case formRowName:
		return "Display name · the agent answers to this"
	case formRowBrainProvider:
		return "Brain provider · (default) inherits Settings"
	case formRowBrainModel:
		if m.brainModels.custom {
			return "Type a model name · ←/→ leaves custom · ctrl+b/ctrl+f move the cursor"
		}
		return "Model · (default) lets the provider choose"
	case formRowWorkerProvider:
		return "Worker · the brain that runs spawned background jobs · (default) inherits Settings"
	case formRowWorkerModel:
		return "Worker model · (default) lets the worker's provider choose"
	case formRowTTSProvider:
		return "TTS provider · (default) inherits Settings"
	case formRowVoice:
		return "Voice · ←/→ pick · p preview/stop"
	case formRowVoiceLanguage:
		if !m.voiceLanguageUsable() {
			return "Language · this catalog has one"
		}
		return "Language · ←/→ narrows the Voice row · (all) shows every voice"
	case formRowTier:
		return "Tier · qwen3-tts only"
	case formRowDirectory:
		return m.directoryRowHelp()
	default:
		return "System prompt · enter opens the editor · Tab inserts {variables}"
	}
}

// canonicalizeBrainModel rewrites the current value to the catalog spelling so
// a hand-edited yaml ("Llama3") lines up with a discovered entry.
func (m *personasModel) canonicalizeBrainModel() {
	if m.brainModels.custom {
		return
	}
	v := m.stackModelValue()
	if v == "" {
		return
	}
	models, _ := m.brainModelCatalog(m.effectiveStackBrainProvider())
	for _, name := range models {
		if strings.EqualFold(name, v) && name != v {
			m.brainModelInput.SetValue(name)
			return
		}
	}
}

// clampSelectedModel drops a model that the new brain provider does not offer,
// mirroring clampSelectedVoice. Two values survive on purpose: free text typed
// under (custom) — the escape hatch may name anything, including a model no
// provider reports — and any value chosen while the catalog is still unknown
// (pending or failed scan), because "unknown" must not read as "invalid".
func (m *personasModel) clampSelectedModel() {
	if m.brainModels.custom {
		return
	}
	v := m.stackModelValue()
	if v == "" {
		return
	}
	models, ok := m.brainModelCatalog(m.effectiveStackBrainProvider())
	if !ok {
		return
	}
	for _, name := range models {
		if strings.EqualFold(name, v) {
			m.brainModelInput.SetValue(name)
			return
		}
	}
	m.brainModelInput.SetValue("")
}

// stackModelLabel is the chip shown on the Model row.
func (m personasModel) stackModelLabel() string {
	list := m.stackModelList()
	idx := m.stackModelIndex()
	if idx < 0 || idx >= len(list) {
		return stackDefaultLabel
	}
	return list[idx]
}

// stackModelNote explains the row when there is nothing to pick: scanning,
// a failed scan, or a provider that reports no model list at all.
func (m personasModel) stackModelNote() string {
	switch {
	case m.brainModels.loading:
		return "scanning models…"
	case m.brainModels.err != "":
		return "model scan failed · (default) or (custom)"
	case !m.brainModels.loaded:
		return ""
	}
	provider := m.effectiveStackBrainProvider()
	models, scanned := m.brainModelCatalog(provider)
	if scanned && len(models) > 0 {
		return ""
	}
	if strings.EqualFold(provider, "claude") {
		return "claude reports no model list · (default) or (custom)"
	}
	if provider == "" || !scanned {
		return "no models discovered · (default) or (custom)"
	}
	return "no " + provider + " models discovered · is it running?"
}

// stackModelRowValue renders the Model row: the picker chip, the free text
// field when (custom) owns the row, and the scan note.
//
// Everything is sized to the row's share of the box interior. Without that the
// row overflowed and formBox truncated the tail: a long ollama name lost its
// closing chevron, and the note vanished entirely behind the text field, which
// pads itself out to its full Width.
func (m personasModel) stackModelRowValue() string {
	// formBox renders body lines at formBoxWidth()-4; stackLines spends the row
	// mark, a padded label, and a separating space on top of that. Everything
	// below is laid out left to right against what is left.
	remaining := max(m.formBoxWidth()-(4+2+stackLabelWidth+1), 12)
	note := m.stackModelNote()

	// The chip is the row's primary content, but it yields half the row when
	// the text field or a note has to share. Truncating the label rather than
	// the chip is what keeps "›" on the end of a long ollama name.
	chipRoom := remaining
	if m.brainModels.custom || note != "" {
		chipRoom = max(remaining/2, 12)
	}
	value := "‹ " + ansi.Truncate(m.stackModelLabel(), max(chipRoom-4, 4), "…") + " ›"
	remaining -= ansi.StringWidth(value)

	if m.brainModels.custom && remaining >= 10 {
		// Render through a copy: sized for the space this row actually has,
		// without disturbing the model's own input width.
		field := m.brainModelInput
		width := remaining - 1
		if note != "" {
			// Keep a slice of the row for the note instead of letting the
			// field's own padding push it out of sight. A scan note also
			// outranks the placeholder: an empty field keeps just its cursor
			// so the diagnostic gets the room.
			if field.Value() == "" {
				field.Placeholder = ""
			}
			width = max(min(width, 24), 8)
		}
		field.Width = width
		rendered := ansi.Truncate(trimTrailingSpaces(field.View()), width, "…")
		value += " " + rendered
		remaining -= ansi.StringWidth(rendered) + 1
	}

	if note != "" && remaining >= 6 {
		value += dimStyle.Render("  " + ansi.Truncate(note, remaining-2, "…"))
	}
	return value
}

// trimTrailingSpaces drops the blank padding a text input emits to fill its
// width, so whatever follows it on the row sits next to the text instead of
// being pushed off the end. Padding carrying a style is left alone — the row
// budget already reserves room for the note in that case.
func trimTrailingSpaces(s string) string {
	return strings.TrimRight(s, " ")
}
