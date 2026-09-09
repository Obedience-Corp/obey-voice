//go:build !integration

package cmd

import (
	"context"

	"github.com/Obedience-Corp/obey-voice/internal/speaker"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

// meetingDiarizer resolves speaker.meeting.diarizer into the review engine the
// analyzer should use. A nil result is the default sherpa path — the engine's
// own pyannote + TitaNet Diarize — so nothing is constructed and no external
// CLI is touched unless the key asks for one.
//
// It runs at meeting start, not at finalize: a missing CLI or GGUF has to be
// reported while the user can still fix it, rather than after a recording they
// cannot re-run.
func meetingDiarizer(ctx context.Context, cfg *config.Config) (speaker.Diarizer, error) {
	selected, err := config.MeetingDiarizerFrom(cfg)
	if err != nil {
		return nil, err
	}
	if selected != config.DiarizerSortformer {
		return nil, nil
	}
	return speaker.NewSortformerDiarizer(ctx, speaker.SortformerOptionsFromConfig(cfg, config.ModelsDirFrom(cfg)))
}

// meetingAnalyzer resolves speaker.meeting.diarizer and builds the review
// analyzer on engine with it attached.
//
// Every meeting path goes through here or through meetingAnalyzerWith —
// `meeting record`, `meeting analyze`, and the `serve` finish-a-remote-bundle
// pipeline — so the diarizer selection cannot apply to some of them and
// silently not others. It takes the engine rather than building one so the
// wiring is testable without native models.
func meetingAnalyzer(ctx context.Context, cfg *config.Config, sp speaker.Config, engine speaker.Engine) (*speaker.Analyzer, error) {
	diarizer, err := meetingDiarizer(ctx, cfg)
	if err != nil {
		return nil, err
	}
	return meetingAnalyzerWith(sp, engine, diarizer)
}

// meetingAnalyzerWith is meetingAnalyzer for callers that already resolved the
// diarizer. `meeting record` resolves it at start to fail fast, so re-resolving
// it in the deferred builder would launch the Sortformer CLI probe twice.
func meetingAnalyzerWith(sp speaker.Config, engine speaker.Engine, diarizer speaker.Diarizer) (*speaker.Analyzer, error) {
	return speaker.NewAnalyzerWithDiarizer(sp, engine, diarizer)
}
