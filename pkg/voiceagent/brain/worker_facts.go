package brain

import (
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/continuity"
)

const (
	workerFactLimit = 8
	// workerFactsBytes holds workerFactLimit jobs at their field limits.
	workerFactsBytes        = 6 << 10
	workerFactTaskBytes     = 160
	workerFactProgressBytes = 200
	workerFactStarterBytes  = 48
	workerFactsOpen         = "\n<active_work>"
	workerFactsClose        = "\n</active_work>"
)

// WorkerFactsPrompt renders the host's record of background jobs for a live
// turn. A job the model is not told about is one it will deny, so a described
// job carries its task, how long it has run, what it has done, who started it
// and whether its tools keep returning the same output, behind a lead-in that
// says what the list is. A list of identifiers alone has nothing for the
// lead-in to explain and goes without it. Unfinished jobs past the limit or
// the byte ceiling are counted, so none is silently left out. Provider and
// model names and pricing are deliberately excluded.
func WorkerFactsPrompt(cfg *config.Config, facts []WorkerFact) string {
	rows, footer := listWorkerFacts(facts)
	if len(rows) == 0 {
		return ""
	}
	var lines strings.Builder
	for _, row := range rows {
		lines.WriteString(row.text)
	}
	if !describedFacts(rows) {
		return workerFactsOpen + lines.String() + workerFactsClose
	}
	lines.WriteString(footer)
	leadIn := "\n" + systemPrompt(cfg, "active-work", nil)
	if slices.ContainsFunc(rows, func(r factRow) bool { return r.same }) {
		leadIn += "\n" + systemPrompt(cfg, "active-work-same-output", nil)
	}
	return leadIn + workerFactsOpen + lines.String() + workerFactsClose
}

// ListedWorkerFactIDs are the jobs WorkerFactsPrompt names for facts, in the
// order it names them. A job past the limit, with an invalid record, cut at
// the size ceiling or given way to the footer is counted there, not named.
func ListedWorkerFactIDs(facts []WorkerFact) []string {
	rows, _ := listWorkerFacts(facts)
	ids := make([]string, len(rows))
	for i, row := range rows {
		ids[i] = row.id
	}
	return ids
}

type factRow struct {
	id, text                    string
	unfinished, described, same bool
}

func describedFacts(rows []factRow) bool {
	return slices.ContainsFunc(rows, func(r factRow) bool { return r.described })
}

// listWorkerFacts chooses the rows WorkerFactsPrompt renders and the footer
// that counts the rest.
func listWorkerFacts(facts []WorkerFact) ([]factRow, string) {
	var rows []factRow
	size := len(workerFactsOpen) + len(workerFactsClose)
	unlisted, cut := 0, false
	for i, fact := range facts {
		state := strings.TrimSpace(fact.State)
		unfinished := state == "running" || state == "queued"
		if i >= workerFactLimit || cut {
			if unfinished {
				unlisted++
			}
			continue
		}
		line := workerFactLine(fact)
		if line == "" {
			continue
		}
		if size+len(line) > workerFactsBytes {
			cut = true
			if unfinished {
				unlisted++
			}
			continue
		}
		rows = append(rows, factRow{id: strings.TrimSpace(fact.ID), text: line, unfinished: unfinished, described: oneLine(fact.Task) != "", same: strings.Contains(line, ` output="`)})
		size += len(line)
	}
	footer := func() string {
		if unlisted == 0 || !describedFacts(rows) {
			return ""
		}
		return fmt.Sprintf("\nunfinished jobs not listed: %d", unlisted)
	}
	// The footer must fit too; rows give way to it from the end.
	for len(rows) > 0 && size+len(footer()) > workerFactsBytes {
		last := rows[len(rows)-1]
		rows, size = rows[:len(rows)-1], size-len(last.text)
		if last.unfinished {
			unlisted++
		}
	}
	return rows, footer()
}

func workerFactLine(fact WorkerFact) string {
	id := strings.TrimSpace(fact.ID)
	state := strings.TrimSpace(fact.State)
	if !safeWorkerFactID(id) || !validWorkerFactState(state) || !validWorkerFactTier(fact.Tier) {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "\njob=%q tier=%q state=%q", id, fact.Tier, state)
	switch starter := oneLine(fact.Starter); {
	case fact.Own:
		b.WriteString(` started_by="you"`)
	case starter != "":
		fmt.Fprintf(&b, " started_by=%q", continuity.Clip(starter, workerFactStarterBytes))
	}
	if state == "running" && fact.Running > 0 {
		fmt.Fprintf(&b, " running_for=%q", workerFactDuration(fact.Running))
	}
	if task := oneLine(fact.Task); task != "" {
		fmt.Fprintf(&b, " task=%q", continuity.Clip(task, workerFactTaskBytes))
	}
	if progress := oneLine(fact.Progress); progress != "" {
		fmt.Fprintf(&b, " progress=%q", continuity.Clip(progress, workerFactProgressBytes))
	}
	if note := SameOutputNote(fact.SameOutput); state == "running" && note != "" {
		fmt.Fprintf(&b, " output=%q", note)
	}
	return b.String()
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// workerFactDuration is a running time as a person would say it. Seconds
// stop mattering after the first minute.
func workerFactDuration(d time.Duration) string {
	unit := func(n int, name string) string {
		if n == 1 {
			return "1 " + name
		}
		return fmt.Sprintf("%d %ss", n, name)
	}
	if d < time.Minute {
		return unit(max(int(d/time.Second), 1), "second")
	}
	hours, minutes := int(d/time.Hour), int(d%time.Hour/time.Minute)
	switch {
	case hours == 0:
		return unit(minutes, "minute")
	case minutes == 0:
		return unit(hours, "hour")
	default:
		return unit(hours, "hour") + " " + unit(minutes, "minute")
	}
}

func safeWorkerFactID(id string) bool {
	if id == "" || len(id) > 128 {
		return false
	}
	for _, r := range id {
		if r > unicode.MaxASCII || !(unicode.IsLetter(r) || unicode.IsDigit(r) || strings.ContainsRune("._:-", r)) {
			return false
		}
	}
	return true
}

func validWorkerFactState(state string) bool {
	return state == "queued" || state == "running" || state == "terminal"
}

func validWorkerFactTier(tier config.WorkerTierName) bool {
	return tier == config.WorkerTierSimple || tier == config.WorkerTierMedium || tier == config.WorkerTierComplex || tier == "legacy"
}
