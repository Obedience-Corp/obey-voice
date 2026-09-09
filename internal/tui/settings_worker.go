package tui

import (
	"fmt"
	"strings"

	"github.com/Obedience-Corp/obey-voice/internal/discovery"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

// The worker is the second brain: it runs the background job a spawn starts,
// with full tools and no one watching. Picking it is part of picking a brain,
// not a buried key — an unchosen worker used to fall through to a remote
// default nobody selected, and then acted on whatever it found on disk.
//
// Provider and model share one section rather than taking a tab each. The
// worker has fewer knobs than the brain, and the tab strip has a width budget:
// two more tabs pushed Speakers off the end of an 80-column strip, which is a
// discoverability bug the settings screen has already been fixed for once.

const (
	workerRowProvider = iota
	workerRowModel
	workerRowDivider
)

// workerItem is one row of the Worker section: a selectable provider, a
// selectable model for the chosen provider, or the rule between them.
type workerItem struct {
	kind  int
	value string
	label string
}

// workerProviderInfos is the brain provider list narrowed to backends that can
// run async jobs, so availability and model catalogs come from one discovery.
func (m settingsModel) workerProviderInfos() []discovery.ProviderInfo {
	var out []discovery.ProviderInfo
	for _, p := range m.providers {
		if config.WorkerProviderAllowed(p.Name) {
			out = append(out, p)
		}
	}
	return out
}

func (m *settingsModel) buildWorkerItems() {
	m.workerItems = nil
	for _, p := range m.workerProviderInfos() {
		label := p.Name
		if !p.Available {
			label += " (not available)"
		}
		m.workerItems = append(m.workerItems, workerItem{kind: workerRowProvider, value: p.Name, label: label})
	}
	m.workerItems = append(m.workerItems, workerItem{kind: workerRowDivider, label: "Worker model"})
	for _, model := range m.workerModelChoices() {
		m.workerItems = append(m.workerItems, workerItem{kind: workerRowModel, value: model, label: "  " + model})
	}
}

// workerModelChoices are the models the chosen worker backend offers, always
// led by "default" so the pin can be cleared.
func (m settingsModel) workerModelChoices() []string {
	out := []string{"default"}
	active := config.VoiceWorkerProviderOf(m.cfg)
	for _, p := range m.workerProviderInfos() {
		if strings.EqualFold(p.Name, active) {
			for _, model := range p.Models {
				if model != "default" {
					out = append(out, model)
				}
			}
			break
		}
	}
	return out
}

// activeWorkerProvider is the provider the Worker rows tick, or "" when the
// operator has not chosen one. It deliberately does not report the inherited
// fallback: an unselected worker must read as unselected.
func (m settingsModel) activeWorkerProvider() string {
	if !config.WorkerSelected(m.cfg) {
		return ""
	}
	return config.VoiceWorkerProviderOf(m.cfg)
}

// activeWorkerModel is the pinned worker model, or "default" when empty.
func (m settingsModel) activeWorkerModel() string {
	if model := strings.TrimSpace(m.cfg.VoiceWorkerModel); model != "" {
		return model
	}
	return "default"
}

// workerStatus is the one-line summary the Worker section shows. It names the
// inherited provider when nothing was chosen so the fallback is visible rather
// than silent.
func (m settingsModel) workerStatus() string {
	if !config.WorkerSelected(m.cfg) {
		return fmt.Sprintf("Worker not set — inheriting %s until you choose", config.VoiceWorkerProviderOf(m.cfg))
	}
	return fmt.Sprintf("Worker: %s · %s", config.VoiceWorkerProviderOf(m.cfg), m.activeWorkerModel())
}

// setWorkerProvider saves the worker backend. Like the brain rows, the live
// config is mutated only after the save succeeds.
func (m *settingsModel) setWorkerProvider(name string) {
	if !m.workerProviderAvailable(name) {
		m.message = fmt.Sprintf("%s is not available on this machine", name)
		return
	}
	save := m.saveConfig
	if save == nil {
		save = config.SetAndSave
	}
	if err := save("voice_worker_provider", name); err != nil {
		m.message = fmt.Sprintf("Failed to save worker: %v", err)
		return
	}
	m.cfg.VoiceWorkerProvider = name
	// A model pinned for the old backend is meaningless for the new one.
	if strings.TrimSpace(m.cfg.VoiceWorkerModel) != "" {
		if err := save("voice_worker_model", ""); err == nil {
			m.cfg.VoiceWorkerModel = ""
		}
	}
	m.buildWorkerItems()
	m.message = fmt.Sprintf("Worker set to %s", name)
}

// setWorkerModel pins a model for the chosen worker; "default" clears the pin.
func (m *settingsModel) setWorkerModel(model string) {
	save := m.saveConfig
	if save == nil {
		save = config.SetAndSave
	}
	value := model
	if value == "default" {
		value = ""
	}
	if err := save("voice_worker_model", value); err != nil {
		m.message = fmt.Sprintf("Failed to save worker model: %v", err)
		return
	}
	m.cfg.VoiceWorkerModel = value
	m.message = fmt.Sprintf("Worker model set to %s", model)
}

func (m settingsModel) workerProviderAvailable(name string) bool {
	for _, p := range m.workerProviderInfos() {
		if strings.EqualFold(p.Name, name) {
			return p.Available
		}
	}
	return false
}
