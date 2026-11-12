package tui

import (
	"strings"

	"github.com/Obedience-Corp/obey-voice/internal/persona"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

func (m personasModel) workerModelCatalog() ([]string, bool) {
	if !m.brainModels.loaded {
		return nil, false
	}
	models, found := m.brainModels.workerByProvider[strings.ToLower(m.effectiveStackWorkerProvider())]
	return models, found
}

// The Worker rows are the second half of choosing this persona's brain: the
// model that runs the background job a spawn starts, with full tools and no
// one watching it. They sit directly under the brain rows so the pair is read
// together, and they follow the same "(default) = inherit Settings" idiom as
// every other stack row.

// effectiveStackWorkerProvider is the provider the Worker model row describes:
// the form selection, the app-level worker when the row is "(default)", and
// finally whatever an unselected app worker resolves to.
func (m personasModel) effectiveStackWorkerProvider() string {
	if provider := providerAt(stackWorkerProviders(), m.workerProviderIdx); provider != "" {
		return provider
	}
	if m.cfg == nil {
		return ""
	}
	return config.VoiceWorkerProviderOf(m.cfg)
}

// stackWorkerModelList builds the Worker model row: (default) + the discovered
// verified models for the effective worker provider. An incompatible saved
// Ollama pin remains visible in its label, without becoming a selectable option.
func (m personasModel) stackWorkerModelList() []string {
	out := []string{stackDefaultLabel}
	models, known := m.workerModelCatalog()
	out = append(out, models...)
	if v := strings.TrimSpace(m.workerModel); v != "" && (!known || m.effectiveStackWorkerProvider() != "ollama") {
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
	return out
}

// workerModelIndex is the row's cursor: 0 when nothing is pinned.
func (m personasModel) workerModelIndex() int {
	v := strings.TrimSpace(m.workerModel)
	if v == "" {
		return 0
	}
	for i, item := range m.stackWorkerModelList() {
		if strings.EqualFold(item, v) {
			return i
		}
	}
	return 0
}

func (m *personasModel) cycleStackWorkerModel(delta int) {
	list := m.stackWorkerModelList()
	idx := (m.workerModelIndex() + delta + len(list)) % len(list)
	if idx == 0 {
		m.workerModel = ""
		return
	}
	m.workerModel = list[idx]
}

// clampSelectedWorkerModel drops a pin the new backend does not offer. A scan
// that has not finished reports "unknown", not "empty" — clamping on unknown
// would wipe a perfectly good saved model.
func (m *personasModel) clampSelectedWorkerModel() {
	v := strings.TrimSpace(m.workerModel)
	if v == "" {
		return
	}
	models, ok := m.workerModelCatalog()
	if !ok {
		return
	}
	for _, name := range models {
		if strings.EqualFold(name, v) {
			return
		}
	}
	m.workerModel = ""
}

// workerModelLabel is what the Worker model row shows.
func (m personasModel) workerModelLabel() string {
	if v := strings.TrimSpace(m.workerModel); v != "" {
		if models, known := m.workerModelCatalog(); known && m.effectiveStackWorkerProvider() == "ollama" && indexOf(models, v) < 0 {
			return v + " (tool support unavailable)"
		}
		return v
	}
	return stackDefaultLabel
}

// formWorker returns the stack step's worker selection ("" fields = inherit).
func (m *personasModel) formWorker() persona.Worker {
	return persona.Worker{
		Provider:   providerAt(stackWorkerProviders(), m.workerProviderIdx),
		Model:      strings.TrimSpace(m.workerModel),
		OllamaHost: m.workerOllamaHost,
	}
}

// prefillWorker seeds the Worker rows from a profile (nil = inherit).
func (m *personasModel) prefillWorker(p *persona.Profile) {
	if p == nil {
		m.work = ""
		m.workerTiers = nil
		m.workerProviderIdx = 0
		m.workerModel = ""
		m.workerOllamaHost = ""
		return
	}
	m.work = strings.ToLower(strings.TrimSpace(p.Brain.Work))
	m.workerTiers = p.Brain.WorkerTiers.Clone()
	m.workerProviderIdx = providerIndex(stackWorkerProviders(), p.Brain.Worker.Provider)
	m.workerModel = strings.TrimSpace(p.Brain.Worker.Model)
	m.workerOllamaHost = strings.TrimSpace(p.Brain.Worker.OllamaHost)
}

// workOptions are the Work row's choices: the backend's default, then own and
// worker. All stay on screen, with a check on the current one.
func workOptions() []string {
	return append([]string{""}, config.WorkChoices...)
}

func (m *personasModel) cycleWork(delta int) {
	options := workOptions()
	idx := (indexOf(options, m.work) + delta + len(options)) % len(options)
	m.work = options[idx]
}

// defaultWork is who runs the work when the persona does not say: it follows
// the brain row, or Settings' brain under "(default)".
func (m personasModel) defaultWork() string {
	provider := providerAt(stackBrainProviders(), m.brainProviderIdx)
	if provider == "" && m.cfg != nil {
		provider = m.cfg.BrainProvider
	}
	return config.DefaultWork(provider)
}

// effectiveWork is who will run the work with the form as it stands.
func (m personasModel) effectiveWork() string {
	if m.work != "" {
		return m.work
	}
	return m.defaultWork()
}

// workRowValue lists every choice inline and checks the current one.
func (m personasModel) workRowValue() string {
	parts := make([]string, 0, len(workOptions()))
	for _, option := range workOptions() {
		label := option
		if option == "" {
			label = stackDefaultLabel + " " + m.defaultWork()
		}
		if option == m.work {
			parts = append(parts, statusStyle.Render("✓ "+label))
		} else {
			parts = append(parts, dimStyle.Render("  "+label))
		}
	}
	return strings.Join(parts, "  ")
}
