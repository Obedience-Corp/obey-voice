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
