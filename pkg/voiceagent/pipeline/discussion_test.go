package pipeline

import (
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/discussion"
)

func TestGroupVoiceRequiresAcceptedAudioBeforeContinuing(t *testing.T) {
	p := &Pipeline{}
	for _, tc := range []struct {
		name                                      string
		voice, audio, degraded, interrupted, want bool
	}{
		{"text delivery", false, false, false, false, true},
		{"no voice sink", true, false, false, false, false},
		{"accepted audio", true, true, false, false, true},
		{"degraded", true, true, true, false, false},
		{"interrupted", true, true, false, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := &completionState{voice: tc.voice, discussion: &discussion.Directive{ID: "group"}}
			metrics := &turnMetrics{degraded: tc.degraded}
			if tc.audio {
				metrics.ttfa = time.Now()
			}
			p.completeAgentResponse(withCompletionState(t.Context(), state), "A useful point.", metrics, tc.interrupted)
			if state.ok != tc.want {
				t.Fatalf("continuation eligible=%v, want %v", state.ok, tc.want)
			}
		})
	}
}
