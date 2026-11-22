package brain

import (
	"fmt"
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
// job carries its task, how long it has run, what it has done and who started
// it, behind a lead-in that says what the list is. A list of identifiers
// alone has nothing for the lead-in to explain and goes without it. Unfinished
// jobs past the limit are counted, so none is silently left out. Provider and
// model names and pricing are deliberately excluded.
func WorkerFactsPrompt(cfg *config.Config, facts []WorkerFact) string {
	unlisted := 0
	if len(facts) > workerFactLimit {
		for _, fact := range facts[workerFactLimit:] {
			if state := strings.TrimSpace(fact.State); state == "running" || state == "queued" {
				unlisted++
			}
		}
		facts = facts[:workerFactLimit]
	}
	var lines strings.Builder
	described := false
	for _, fact := range facts {
		line := workerFactLine(fact)
		if line == "" {
			continue
		}
		if len(workerFactsOpen)+lines.Len()+len(line)+len(workerFactsClose) > workerFactsBytes {
			break
		}
		lines.WriteString(line)
		described = described || oneLine(fact.Task) != ""
	}
	if lines.Len() == 0 {
		return ""
	}
	if !described {
		return workerFactsOpen + lines.String() + workerFactsClose
	}
	if unlisted > 0 {
		fmt.Fprintf(&lines, "\nunfinished jobs not listed: %d", unlisted)
	}
	return "\n" + systemPrompt(cfg, "active-work", nil) + workerFactsOpen + lines.String() + workerFactsClose
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
