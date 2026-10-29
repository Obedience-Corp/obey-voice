package tui

import (
	"context"
	tea "github.com/charmbracelet/bubbletea"
	"sync"
)

// trackedCommand reserves a drain slot before dispatch, but releases it if
// shutdown drops the command. Commands that already started retain their slot
// until completion. The program must cancel lifetime before waiting on wg.
func trackedCommand(lifetime context.Context, wg *sync.WaitGroup, cmd tea.Cmd) tea.Cmd {
	if wg == nil {
		return cmd
	}
	if lifetime == nil {
		lifetime = context.Background()
	}
	wg.Add(1)
	var claim sync.Once
	stop := context.AfterFunc(lifetime, func() { claim.Do(wg.Done) })
	return func() tea.Msg {
		run := false
		claim.Do(func() { run = true })
		stop()
		if !run {
			return nil
		}
		defer wg.Done()
		return cmd()
	}
}
