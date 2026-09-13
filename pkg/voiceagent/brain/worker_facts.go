package brain

import (
	"fmt"
	"strings"
	"unicode"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

// WorkerFactsPrompt renders only bounded canonical job facts for a live turn.
// It deliberately excludes task text, provider/model names, and pricing.
func WorkerFactsPrompt(facts []WorkerFact) string {
	if len(facts) == 0 {
		return ""
	}
	if len(facts) > 8 {
		facts = facts[:8]
	}
	var b strings.Builder
	b.WriteString("\n<active_work>")
	for _, fact := range facts {
		id := strings.TrimSpace(fact.ID)
		state := strings.TrimSpace(fact.State)
		if !safeWorkerFactID(id) || !validWorkerFactState(state) || !validWorkerFactTier(fact.Tier) {
			continue
		}
		line := fmt.Sprintf("\njob=%q tier=%q state=%q", id, fact.Tier, state)
		if b.Len()+len(line)+len("\n</active_work>") > 2048 {
			break
		}
		b.WriteString(line)
	}
	b.WriteString("\n</active_work>")
	return b.String()
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
