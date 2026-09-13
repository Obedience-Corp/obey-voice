package tui

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/Obedience-Corp/obey-voice/internal/workjournal"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/work"
	tea "github.com/charmbracelet/bubbletea"
)

type workResultController interface {
	WorkResultAction(work.WorkScope, string, string) error
}

func (m *conversationModel) runWorkCommand(args []string) tea.Cmd {
	m.recoverWorkNow()
	if len(args) == 0 || (len(args) == 1 && args[0] == "all") {
		m.workActivity, m.activityFocused, m.workFilter = true, true, ""
		m.refreshActivity()
		return nil
	}
	if len(args) == 1 && args[0] == "logs" {
		id := m.sessionID
		dir := filepath.Join(config.SessionsDir(), id+".work")
		m.commandNotice(fmt.Sprintf("Diagnostics: %s · persisted only. CLI: obey-voice sessions inspect %s --work; obey-voice sessions events %s --json", dir, id, id))
		return nil
	}
	if len(args) == 1 {
		if !m.hasWorkID(args[0]) {
			m.commandError("No retained work item " + args[0])
			return nil
		}
		m.workActivity, m.activityFocused, m.workFilter = true, true, args[0]
		if m.workDisplay.seen == nil {
			m.workDisplay.seen = map[string]bool{}
		}
		for _, rows := range [][]work.ResultView{m.workDisplay.snapshot.Pending, m.workDisplay.snapshot.Communicated} {
			for _, r := range rows {
				if r.Outcome.JobID == args[0] || r.Outcome.ResultID == args[0] {
					m.workDisplay.seen[r.Outcome.ResultID] = true
				}
			}
		}
		m.refreshActivity()
		m.activityViewport.GotoTop()
		return nil
	}
	if len(args) != 2 {
		m.commandError("usage: /work [job|all|cancel job|dismiss job|speak job|replay job|logs]")
		return nil
	}
	action, id := args[0], args[1]
	var err error
	switch action {
	case "cancel":
		controller, ok := m.deps.runner.(interface {
			CancelWork(string) error
			StopWorkers() error
		})
		if !ok {
			m.commandError("Worker cancellation is unavailable")
			return nil
		}
		if id == "all" {
			err = controller.StopWorkers()
		} else {
			err = controller.CancelWork(id)
		}
	case "dismiss", "speak", "replay":
		controller, ok := m.deps.runner.(workResultController)
		if !ok {
			m.commandError("Result controls are unavailable")
			return nil
		}
		err = controller.WorkResultAction(m.workScope, id, action)
	default:
		m.commandError("Unknown work action; use cancel, dismiss, speak or replay")
		return nil
	}
	if err != nil {
		m.commandError(err.Error())
		return nil
	}
	switch action {
	case "cancel":
		m.commandNotice("Cancellation requested; termination remains pending until confirmed.")
	case "dismiss":
		m.commandNotice("Result dismissed; worker execution was not cancelled.")
	default:
		m.commandNotice("Result queued for speech at the next safe conversation boundary.")
	}
	m.recoverWorkNow()
	m.refreshActivity()
	if action == "speak" || action == "replay" {
		return m.reconcileBackground()
	}
	return nil
}

func (m *conversationModel) hasWorkID(id string) bool {
	for _, j := range m.workDisplay.snapshot.Jobs {
		if j.Key.JobID == id {
			return true
		}
	}
	for _, rows := range [][]work.ResultView{m.workDisplay.snapshot.Pending, m.workDisplay.snapshot.Communicated} {
		for _, r := range rows {
			if r.Outcome.JobID == id || r.Outcome.ResultID == id {
				return true
			}
		}
	}
	if c, ok := m.deps.runner.(workerController); ok {
		for _, j := range c.Workers() {
			if j.ID == id {
				return true
			}
		}
	}
	return false
}

func (m conversationModel) renderWorkActivity() string {
	var out strings.Builder
	snap := workjournal.NewNormalizer(m.cfg).Snapshot(m.workDisplay.snapshot)
	fmt.Fprintf(&out, "Work · %s · revision %d / saved %d\n", snap.Scope.SessionID, snap.Revision, snap.PersistedRevision)
	if !snap.Scope.Valid() {
		out.WriteString("Conversation result tracking is off. /workers shows available execution controls.\n")
	}
	filter := m.workFilter
	for _, j := range snap.Jobs {
		if filter != "" && j.Key.JobID != filter {
			continue
		}
		fmt.Fprintf(&out, "\n%s · %s · %s\nTier: %s · %s/%s · origin persona %s\n", j.Key.JobID, j.Task, j.State, workTierLabel(j.Config["worker_tier"]), j.Provider, j.Model, j.Config["persona"])
		for _, raw := range m.workDisplay.snapshot.Jobs {
			if raw.Key.JobID == j.Key.JobID {
				var plan config.WorkerPlan
				if json.Unmarshal([]byte(raw.Config["worker_plan"]), &plan) == nil {
					fmt.Fprintf(&out, "Limits: %d requests · %d tools · %ds runtime · policy %s\n", plan.Limits.ModelRequests, plan.Limits.ToolCalls, plan.Limits.RuntimeSeconds, plan.Policy.ChargePolicy)
				}
			}
		}
		for _, row := range m.workTracker().Rows(j.Key.JobID) {
			fmt.Fprintf(&out, "  %s\n", row.String())
			if row.Last != "" {
				fmt.Fprintf(&out, "  %s\n", row.Last)
			}
		}
	}
	for _, rows := range [][]work.ResultView{snap.Pending, snap.Communicated} {
		for _, r := range rows {
			if filter != "" && r.Outcome.JobID != filter && r.Outcome.ResultID != filter {
				continue
			}
			fmt.Fprintf(&out, "\n%s · result %s · %s / %s\n%s\n", r.Outcome.JobID, r.Outcome.ResultID, r.State, r.AnnouncementState, r.Outcome.Text)
			fmt.Fprintf(&out, "Origin persona: %s · tier %s · %s/%s\n", r.Outcome.Config["persona"], workTierLabel(r.Outcome.Config["worker_tier"]), r.Outcome.Provider, r.Outcome.Model)
			if r.HoldReason != "" {
				fmt.Fprintf(&out, "Held: %s\n", r.HoldReason)
			}
			if r.LastAttempt != nil {
				fmt.Fprintf(&out, "Attempt %s: %s · playback complete %t · addressed %t\n", r.LastAttempt.AttemptID, r.LastAttempt.Reason, r.LastAttempt.Receipt.PlaybackComplete, r.LastAttempt.Addressed)
			}
		}
	}
	if !snap.Scope.Valid() {
		if controller, ok := m.deps.runner.(workerController); ok {
			for _, item := range controller.Workers() {
				if filter != "" && item.ID != filter {
					continue
				}
				fmt.Fprintf(&out, "\n%s · %s · %s · %s/%s\n%s\n%s\nTermination: %s\n", item.ID, item.Task, item.State, item.Provider, item.Model, item.Activity, item.Result, item.Termination)
			}
		}
	}
	out.WriteString("\n/work cancel ID · /work dismiss ID · /work speak ID · /work replay ID\n/work logs · Esc clears detail, then returns to Chat")
	if m.journalIncomplete {
		out.WriteString("\nDiagnostic history incomplete; canonical results remain available.")
	}
	return workjournal.NewNormalizer(m.cfg).Text(out.String(), 64<<10)
}

func (m *conversationModel) recoverWorkNow() { m.recoverWork(time.Now(), true) }

func (m conversationModel) workResultText(id string) string {
	for _, rows := range [][]work.ResultView{m.workDisplay.snapshot.Pending, m.workDisplay.snapshot.Communicated} {
		for _, r := range rows {
			if r.Outcome.ResultID == id {
				text := "Background result · " + r.Task + ": " + r.Outcome.Text
				if r.Outcome.SourceRef != "" {
					text += " · source: " + r.Outcome.SourceRef
				}
				return text
			}
		}
	}
	return ""
}
func (m conversationModel) latestReadableWorkResult() string {
	for i := len(m.items) - 1; i >= 0; i-- {
		item := m.items[i]
		if item.kind == chatItemAgent && strings.TrimSpace(item.spoken) != "" {
			return ""
		}
		if item.workResultID != "" {
			return m.workResultText(item.workResultID)
		}
	}
	return ""
}
func (m conversationModel) readableWorkResults() []string {
	var results []string
	seen := map[string]bool{}
	for _, item := range m.items {
		id := item.workResultID
		if id != "" && !seen[id] {
			seen[id] = true
			if text := m.workResultText(id); text != "" {
				results = append(results, text)
			}
		}
	}
	return results
}
