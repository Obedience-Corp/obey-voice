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
	cw := correct.NewCustomWords(report.Terms, report.Threshold)
	cw.SetCandidateBand(cfg.STTPostprocessCandidateThreshold)
	return cw, report
}

// StageReport says which correction stages config enabled.
type StageReport struct {
	Fillers     bool
	CustomWords TermReport
	Numbers     bool
	// Postprocess describes the optional model stage; Model is the running
	// stage itself when it was built, for its counters.
	Postprocess PostprocessReport
	Model       *correct.Model
	// Stages lists the enabled stages in the order they run.
	Stages []string
}

// StagesFromConfig builds the correction chain: fillers first, so hesitation
// sounds never split a name; then custom words; then the optional model,
// which patches from the candidates custom words found; then numbers.
func StagesFromConfig(cfg *config.Config) (correct.Chain, StageReport) {
	var stages []correct.Stage
	report := StageReport{}
	if cfg.STTFillerRemoval {
		stages = append(stages, correct.Fillers{})
		report.Fillers = true
	}
	cw, terms := CustomWordsFromConfig(cfg)
	report.CustomWords = terms
	if !cw.Empty() {
		stages = append(stages, cw)
	}
	if cfg.STTPostprocessEnabled {
		m, rep := PostprocessFromConfig(cfg, cw, terms.Terms)
		report.Postprocess = rep
		if m != nil {
			stages = append(stages, m)
			report.Model = m
		}
	}
	if cfg.STTNumbers {
		stages = append(stages, correct.Numbers{})
		report.Numbers = true
	}
	for _, s := range stages {
		report.Stages = append(report.Stages, s.Name())
	}
	return correct.New(stages...), report
}

// withConfiguredCorrection wraps p with the configured stages; when none is
// enabled, p is returned as is.
func withConfiguredCorrection(p Provider, cfg *config.Config) Provider {
	chain, _ := StagesFromConfig(cfg)
	if chain.Len() == 0 {
		return p
	}
	return WithCorrection(p, chain, nil)
}
