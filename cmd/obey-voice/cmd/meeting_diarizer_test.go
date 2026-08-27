//go:build !integration

package cmd

import (
	"context"
	"strings"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

func TestMeetingDiarizerSelection(t *testing.T) {
	tests := []struct {
		name     string
		diarizer string
		wantErr  string
	}{
		{
			name:     "unknown engine is refused",
			diarizer: "pyannote",
			wantErr:  "unsupported speaker.meeting.diarizer",
		},
		{
			// The CLI is not installed here, and that is the point: selecting
			// Sortformer has to fail at meeting start, not at finalize.
			name:     "sortformer without the CLI fails at start",
			diarizer: config.DiarizerSortformer,
			wantErr:  "transcribe.cpp",
		},
		{name: "unset stays on sherpa", diarizer: ""},
		{name: "sherpa", diarizer: config.DiarizerSherpa},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.Config{ModelsDir: t.TempDir(), TranscribeCPPBinary: "obey-voice-absent-transcribe-cli"}
			cfg.Speaker.Meeting.Diarizer = tt.diarizer

			diarizer, err := meetingDiarizer(context.Background(), cfg)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("meetingDiarizer() error = %v, want it to mention %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("meetingDiarizer() error = %v", err)
			}
			// nil means the analyzer keeps the engine's own review path: the
			// default must never reach for an external CLI.
			if diarizer != nil {
				t.Fatalf("meetingDiarizer() = %T, want the default sherpa path (nil)", diarizer)
			}
		})
	}
}
