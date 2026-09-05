package stt

import (
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/stt/correct"
)

// TermReport says how the custom-words list was assembled from config.
type TermReport struct {
	// Terms is the merged list the matcher runs, explicit entries first.
	Terms []correct.Term
	// Fuzzy names the terms that also match near-misses.
	Fuzzy []string
	// Ignored lists config entries that could not be parsed.
	Ignored []string
	// Dropped counts terms beyond correct.MaxTerms.
	Dropped   int
	Threshold float64
}

// CustomWordsFromConfig builds the custom-words stage: entries from
// stt_custom_words first, then the agent's and every persona's display name
// when stt_custom_words_from_personas is on. The stage is empty when neither
// yields a term.
func CustomWordsFromConfig(cfg *config.Config) (*correct.CustomWords, TermReport) {
	report := TermReport{Threshold: cfg.STTWordCorrectionThreshold}
	if report.Threshold <= 0 {
		report.Threshold = correct.DefaultThreshold
	}
	explicit := make([]correct.Term, 0, len(cfg.STTCustomWords))
	for _, entry := range cfg.STTCustomWords {
		t, err := correct.ParseTerm(entry)
		if err != nil {
			report.Ignored = append(report.Ignored, entry)
			continue
		}
		explicit = append(explicit, t)
	}
	var derived []correct.Term
	if cfg.STTCustomWordsFromPersonas {
		names := make([]string, 0, 1+len(cfg.PersonaDisplayNames))
		names = append(names, cfg.AgentName)
		names = append(names, cfg.PersonaDisplayNames...)
		derived = correct.PersonaTerms(names)
	}
	report.Terms, report.Dropped = correct.Merge(explicit, derived)
	for _, t := range report.Terms {
		if t.Fuzzy && correct.FuzzyEligible(t.Text) {
			report.Fuzzy = append(report.Fuzzy, t.Text)
		}
	}
	return correct.NewCustomWords(report.Terms, report.Threshold), report
}

// withConfiguredCorrection wraps p with the custom-words stage when config
// yields any term; otherwise p is returned as is.
func withConfiguredCorrection(p Provider, cfg *config.Config) Provider {
	cw, _ := CustomWordsFromConfig(cfg)
	if cw.Empty() {
		return p
	}
	return WithCorrection(p, correct.New(cw), nil)
}
