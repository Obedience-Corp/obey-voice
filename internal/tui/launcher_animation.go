package tui

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

type launcherTickMsg struct{ id uint64 }

// syncLauncherAnimation runs after every App update, covering initial sizing,
// resize, and every route back to the menu. At most one current tick is pending.
// The generation rejects an old timer after a quick leave/re-enter or resize.
func (a *App) syncLauncherAnimation() tea.Cmd {
	visible := !a.quitting && a.screen == screenLauncher &&
		!a.launcher.reducedMotion && launcherMarkRows(a.launcher.width, a.launcher.height) > 0
	if !visible {
		if a.launcherTicking {
			a.launcherTickID++
			a.launcherTicking = false
		}
		return nil
	}
	if a.launcherTicking {
		return nil
	}
	a.launcherTicking = true
	a.launcherTickID++
	id := a.launcherTickID
	return tea.Tick(80*time.Millisecond, func(time.Time) tea.Msg {
		return launcherTickMsg{id: id}
	})
}
