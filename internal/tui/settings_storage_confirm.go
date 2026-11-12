package tui

import (
	"context"
	"fmt"
	"os"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

// storagePlanMsg is the check of a move the user asked for.
type storagePlanMsg struct {
	seq  int64
	plan config.MovePlan
	err  error
}

func (m settingsModel) storageConfirming() bool {
	return m.storage != nil && m.storage.confirm != nil
}

// armStorageMove checks a move to dest off the UI thread; the confirmation
// shows what the check found.
func (m *settingsModel) armStorageMove(dest string) tea.Cmd {
	dest = strings.TrimSpace(dest)
	switch {
	case dest == "":
		m.message = "Cancelled"
		return nil
	case m.storage.moving():
		m.message = "A move is still finishing"
		return nil
	case m.storageMoveBlocked() != "":
		m.message = m.storageMoveBlocked()
		return nil
	}
	seq := storageSeq.Add(1)
	m.storage.checking, m.storage.confirm = seq, nil
	m.message = "Checking the move…"
	screenCfg, plan, storage := m.cfg.Clone(), m.storage.plan, m.storage
	if plan == nil {
		plan = config.PlanModelsMove
	}
	return func() tea.Msg {
		cfg := storage.moveConfig(screenCfg)
		result, err := plan(context.Background(), cfg, dest, config.MoveOptions{Personas: storage.movePersonas(cfg)})
		return storagePlanMsg{seq: seq, plan: result, err: err}
	}
}

func (m *settingsModel) applyStoragePlan(msg storagePlanMsg) {
	s := m.storage
	if msg.seq != s.checking {
		return
	}
	s.checking = 0
	switch {
	case msg.err != nil:
		m.message = "Could not check the move: " + msg.err.Error()
	case len(msg.plan.Refusals) > 0:
		m.message = "Models can't be moved: " + strings.Join(msg.plan.Refusals, "; ")
	default:
		plan := msg.plan
		s.confirm = &plan
		m.message = ""
	}
}

// updateStorageMoveConfirm answers the checked move. Only y starts it.
func (m settingsModel) updateStorageMoveConfirm(msg tea.KeyMsg) (settingsModel, tea.Cmd) {
	plan := m.storage.confirm
	m.storage.confirm = nil
	if msg.String() != "y" && msg.String() != "Y" {
		m.message = "Kept the models in " + config.ModelsDirFrom(m.cfg)
		return m, nil
	}
	return m, m.startStorageMove(plan.To)
}

func (m settingsModel) confirmStorageMoveLines() []string {
	plan := m.storage.confirm
	to := plan.To
	if plan.Dest != "" && plan.Dest != plan.To {
		to += " (files land in " + plan.Dest + ")"
	}
	how := "Copied to the other drive and checked; the old folder is removed after."
	if plan.SameVolume {
		how = "Renamed in place on the same drive."
	}
	body := []string{
		normalStyle.Render(fmt.Sprintf("Move %s in %d files?", config.FormatBytes(plan.Bytes), plan.Files)),
		"",
		dimStyle.Render("From  " + plan.From),
		dimStyle.Render("To    " + to),
		dimStyle.Render(how),
	}
	if len(plan.Unrecognized) > 0 {
		body = append(body, "", normalStyle.Render("Not used by your settings or personas:"),
			dimStyle.Render("these move too, and the old folder is then kept, not deleted"))
		for i, e := range plan.Unrecognized {
			if i == 5 {
				body = append(body, dimStyle.Render(fmt.Sprintf("  and %d more", len(plan.Unrecognized)-5)))
				break
			}
			body = append(body, dimStyle.Render(fmt.Sprintf("  %s  %s", e.Name, config.FormatBytes(e.Bytes))))
		}
	}
	if len(plan.RewriteKeys) > 0 {
		body = append(body, dimStyle.Render("Also points "+strings.Join(plan.RewriteKeys, ", ")+" at the new folder."))
	}
	body = append(body,
		"",
		dimStyle.Render("Cancel works until the new folder is saved. Leaving Settings or quitting"),
		dimStyle.Render("cancels too; quitting waits for the cleanup."),
		"",
		normalStyle.Render("Press ")+errorStyle.Render("y")+normalStyle.Render(" to move · any other key keeps them where they are"),
	)
	box := formBoxStyled(errorStyle.Render("Confirm move"), body, m.settingsBoxWidth(), errorStyle)
	return append([]string{""}, box...)
}

// storageWaitSay prints to the terminal after the program has ended; tests
// replace it.
var storageWaitSay = func(text string) { fmt.Fprintln(os.Stdout, text) }
