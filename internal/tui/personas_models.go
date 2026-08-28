package tui

import (
	"context"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

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
	m.brainModels.custom = false
	m.brainModels.byProvider = nil
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
func (m *personasModel) cycleStackModel(delta int) {
	list := m.stackModelList()
	idx := (m.stackModelIndex() + delta + len(list)) % len(list)
	switch {
	case idx == 0:
		m.brainModels.custom = false
		m.brainModelInput.SetValue("")
		m.brainModelInput.Blur()
	case idx == len(list)-1:
		m.brainModels.custom = true
		m.brainModelInput.Focus()
		m.brainModelInput.CursorEnd()
		m.message = "Type a model name · ←/→ leaves custom · ctrl+b/ctrl+f move the cursor"
	default:
		m.brainModels.custom = false
		m.brainModelInput.SetValue(list[idx])
		m.brainModelInput.Blur()
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
func (m personasModel) stackModelRowValue() string {
	value := "‹ " + m.stackModelLabel() + " ›"
	if m.brainModels.custom {
		value += " " + m.brainModelInput.View()
	}
	if note := m.stackModelNote(); note != "" {
		value += dimStyle.Render("  " + note)
	}
	return value
}
