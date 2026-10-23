package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/pipeline"
)

type workerController interface {
	Workers() []pipeline.WorkerInfo
	SetWorkersEnabled(bool)
	CancelWork(string) error
	StopWorkers() error
}

type workersTickMsg struct{ token *int }

type workersModel struct {
	cfg                           *config.Config
	controller                    workerController
	saveConfig                    func(string, interface{}) error
	items                         []pipeline.WorkerInfo
	cursor, offset, width, height int
	returnScreen                  screen
	detailID, message             string
	confirmAll                    bool
	detail                        viewport.Model
	token                         *int
}

func newWorkers(cfg *config.Config, controller workerController) workersModel {
	m := workersModel{cfg: cfg, controller: controller, returnScreen: screenLauncher,
		detail: viewport.New(80, 12), token: new(int)}
	m.refresh()
	return m
}

func (m workersModel) tick() tea.Cmd {
	return tea.Tick(time.Second, func(time.Time) tea.Msg { return workersTickMsg{m.token} })
}

func (m *workersModel) refresh() {
	selected := ""
	if m.cursor >= 2 && m.cursor-2 < len(m.items) {
		selected = m.items[m.cursor-2].ID
	}
	if m.controller != nil {
		m.items = m.controller.Workers()
	}
	for i, item := range m.items {
		if item.ID == selected {
			m.cursor = i + 2
			break
		}
	}
	m.cursor = min(m.cursor, len(m.items)+1)
	m.ensureVisible()
	m.refreshDetail()
}

func (m workersModel) rows() int { return max(1, (max(m.height, 10)-8)/2) }
func (m *workersModel) ensureVisible() {
	if m.cursor < m.offset {
		m.offset = m.cursor
	}
	if m.cursor >= m.offset+m.rows() {
		m.offset = m.cursor - m.rows() + 1
	}
}

func (m *workersModel) refreshDetail() {
	m.detail.Width, m.detail.Height = max(1, m.width-4), max(1, m.height-8)
	for _, item := range m.items {
		if item.ID != m.detailID {
			continue
		}
		offset := m.detail.YOffset
		m.detail.SetContent(formatWorkerDetail(item, m.detail.Width))
		m.detail.SetYOffset(offset)
		return
	}
	if m.detailID != "" {
		m.detail.SetContent("This task is no longer in the current conversation.")
	}
}

func (m *workersModel) openDetail(id string) {
	for i, item := range m.items {
		if item.ID != id {
			continue
		}
		m.cursor = i + 2
		m.ensureVisible()
		m.detailID = id
		m.detail.GotoTop()
		m.refreshDetail()
		return
	}
}

func (m workersModel) Update(msg tea.Msg) (workersModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.ensureVisible()
		m.refreshDetail()
	case workersTickMsg:
		if msg.token != m.token {
			return m, nil
		}
		m.refresh()
		return m, m.tick()
	case tea.KeyMsg:
		if m.confirmAll {
			m.confirmAll = false
			if msg.String() == "y" {
				if m.controller != nil {
					if err := m.controller.StopWorkers(); err != nil {
						m.message = "Stop failed: " + err.Error()
					} else {
						m.message = "Stop requested for all active tasks; check details for termination."
					}
				}
				m.refresh()
			}
			return m, nil
		}
		switch msg.String() {
		case "esc", "q":
			if m.detailID != "" {
				m.detailID = ""
				return m, nil
			}
			return m, func() tea.Msg { return switchScreenMsg(m.returnScreen) }
		case "t":
			m.toggle()
		case "x":
			m.requestStopAll()
		case "s":
			m.stopSelected()
		case "enter", " ":
			if m.detailID != "" {
				break
			}
			switch m.cursor {
			case 0:
				m.toggle()
			case 1:
				m.requestStopAll()
			default:
				if m.cursor-2 < len(m.items) {
					m.detailID = m.items[m.cursor-2].ID
					m.detail.GotoTop()
					m.refreshDetail()
				}
			}
		case "up", "k":
			if m.detailID == "" {
				m.cursor = max(0, m.cursor-1)
				m.ensureVisible()
				return m, nil
			}
		case "down", "j":
			if m.detailID == "" {
				m.cursor = min(len(m.items)+1, m.cursor+1)
				m.ensureVisible()
				return m, nil
			}
		}
		if m.detailID != "" {
			var cmd tea.Cmd
			m.detail, cmd = m.detail.Update(msg)
			return m, cmd
		}
	}
	return m, nil
}

// Save first: a failed write must leave both the displayed and live permission unchanged.
func saveWorkersEnabled(cfg *config.Config, enabled bool, save func(string, interface{}) error, apply func(bool)) error {
	if save == nil {
		save = config.SetAndSave
	}
	if err := save("voice_workers_disabled", !enabled); err != nil {
		return err
	}
	cfg.VoiceWorkersDisabled = !enabled
	if apply != nil {
		apply(enabled)
	}
	return nil
}

func (m *workersModel) toggle() {
	var apply func(bool)
	if m.controller != nil {
		apply = m.controller.SetWorkersEnabled
	}
	enabled := m.cfg.VoiceWorkersDisabled
	if err := saveWorkersEnabled(m.cfg, enabled, m.saveConfig, apply); err != nil {
		m.message = "Failed to save worker preference: " + err.Error()
		return
	}
	m.message = workersPermissionMessage(enabled)
}

func workersPermissionMessage(enabled bool) string {
	if enabled {
		return "Workers allowed — saved for this and future chats."
	}
	return "Workers off — existing tasks may finish. Use Stop all to cancel them."
}

func (m *workersModel) requestStopAll() {
	for _, item := range m.items {
		if item.Active() {
			m.confirmAll = true
			return
		}
	}
	m.message = "No active tasks to stop."
}

func (m *workersModel) stopSelected() {
	id := m.detailID
	if id == "" && m.cursor >= 2 && m.cursor-2 < len(m.items) {
		id = m.items[m.cursor-2].ID
	}
	for _, item := range m.items {
		if item.ID != id {
			continue
		}
		if !item.Active() {
			m.message = "This task has already ended."
			return
		}
		if m.controller != nil {
			if err := m.controller.CancelWork(id); err != nil {
				m.message = "Stop failed: " + err.Error()
			} else {
				m.message = "Stop requested; check task details for termination."
			}
		}
		m.refresh()
		return
	}
	m.message = "Select a task to stop."
}

func workerProvider(item pipeline.WorkerInfo) string {
	provider := item.Provider
	if provider == "" {
		provider = "unknown provider"
	}
	model := item.Model
	if model == "" {
		model = "default model"
	}
	return provider + " / " + model
}

func safeWorkerText(text string) string {
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' || (r >= 32 && r != 127) {
			return r
		}
		return -1
	}, ansi.Strip(text))
}

func workersCountLabel(running, queued int, enabled bool) string {
	state := "Workers:"
	if !enabled {
		state = "Workers off:"
	}
	if running+queued == 0 {
		if !enabled {
			return "Workers off · /workers"
		}
		return "Workers: idle · /workers"
	}
	return fmt.Sprintf("%s %d running · %d queued · /workers", state, running, queued)
}

func workerFooterLabel(label string, width int) string {
	label = strings.TrimSuffix(label, " · /workers")
	suffix := "/workers · /menu back"
	if lipgloss.Width(label+" · "+suffix) > width {
		label = strings.ReplaceAll(label, "Workers:", "")
		label = strings.ReplaceAll(label, "running", "run")
		label = strings.TrimSpace(label)
	}
	if lipgloss.Width(label+" · "+suffix) <= width {
		return label + " · " + suffix
	}
	room := width - lipgloss.Width(suffix) - 3
	if room > 3 {
		return ansi.Truncate(label, room, "…") + " · " + suffix
	}
	return ansi.Truncate(suffix, width, "…")
}

func (m workersModel) View() string {
	w := m.width
	if w <= 0 {
		w = 80
	}
	var lines []string
	running, queued := 0, 0
	for _, item := range m.items {
		if item.State == "queued" {
			queued++
		} else if item.Active() {
			running++
		}
	}
	summary := strings.TrimSuffix(workersCountLabel(running, queued, !m.cfg.VoiceWorkersDisabled), " · /workers")
	lines = append(lines, headerStyle.Render("  Workers"), dimStyle.Render("  "+summary+" · this conversation"), "")
	if m.detailID != "" {
		lines = append(lines, strings.Split(m.detail.View(), "\n")...)
	} else {
		labels := []string{"Allow background workers: " + onOff(!m.cfg.VoiceWorkersDisabled), "Stop all active tasks"}
		details := []string{"Off blocks new jobs; existing tasks may finish.", "Request cancellation for running and queued tasks."}
		for _, item := range m.items {
			labels = append(labels, item.Task+" · "+item.State)
			detail := workerProvider(item) + " · " + roundSeconds(item.Elapsed)
			if summary := workerActivitySummary(item.Activity); summary != "" {
				detail += " · " + summary
			}
			details = append(details, detail)
		}
		for i := m.offset; i < min(len(labels), m.offset+m.rows()); i++ {
			prefix := "  "
			if i == m.cursor {
				prefix = "› "
			}
			line := ansi.Truncate(prefix+strings.Join(strings.Fields(safeWorkerText(labels[i])), " "), w, "…")
			if i == m.cursor {
				line = chipStyle.Render(line)
			}
			lines = append(lines, line, dimStyle.Render("    "+strings.Join(strings.Fields(safeWorkerText(details[i])), " ")))
		}
		if len(m.items) == 0 {
			lines = append(lines, "  No background tasks in this conversation.")
		}
	}
	message := m.message
	if m.confirmAll {
		message = "Stop all running and queued tasks? y confirm · any other key cancels"
	}
	lines = append(lines, "", statusStyle.Render("  "+safeWorkerText(message)))
	help := "  ↑/↓ select · enter open · s stop task · x stop all · t toggle · esc back"
	if m.detailID != "" {
		help = "  ↑/↓ scroll · s stop task · x stop all · t toggle · esc list"
	}
	lines = append(lines, dimStyle.Render(help))
	for i, line := range lines {
		lines[i] = ansi.Truncate(line, w, "…")
	}
	// Keep the footer visible in short terminal splits.
	if m.height > 0 && len(lines) > m.height {
		keep := max(0, m.height-2)
		lines = append(lines[:keep], lines[len(lines)-2:]...)
		lines = lines[:min(len(lines), m.height)]
	}
	return lipgloss.JoinVertical(lipgloss.Left, lines...)
}
