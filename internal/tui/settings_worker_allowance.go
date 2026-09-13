package tui

import (
	"context"
	"fmt"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/usage"
	tea "github.com/charmbracelet/bubbletea"
)

type workerAllowanceMsg struct {
	snapshot usage.Snapshot
	err      error
}

func (m *settingsModel) inspectWorkerAllowance() tea.Cmd {
	if m.workerAllowanceLoading {
		return nil
	}
	m.workerAllowanceLoading = true
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		snapshot, err := config.WorkerAllowance(ctx)
		return workerAllowanceMsg{snapshot: snapshot, err: err}
	}
}

func (m settingsModel) workerAllowanceValue() string {
	if m.workerAllowanceLoading {
		return "checking…"
	}
	if m.workerAllowance == nil {
		if m.workerAllowanceErr != "" {
			return "unavailable"
		}
		return "inspect"
	}
	s := m.workerAllowance
	prefix := ""
	if s.Frozen {
		prefix = "strict dispatch frozen · "
	}
	if s.DailyMicroUSD == nil {
		return prefix + fmt.Sprintf("reserved %s · settled %s · unknown %d", workerMicroAmount(s.ReservedMicroUSD), workerMicroAmount(s.SettledMicroUSD), s.UnknownCount)
	}
	return prefix + fmt.Sprintf("%s daily · reserved %s · settled %s · unknown %d", workerMicroAmount(s.DailyMicroUSD), workerMicroAmount(s.ReservedMicroUSD), workerMicroAmount(s.SettledMicroUSD), s.UnknownCount)
}

func workerMicroAmount(v *int64) string {
	if v == nil {
		return "unknown"
	}
	return fmt.Sprint(*v)
}
