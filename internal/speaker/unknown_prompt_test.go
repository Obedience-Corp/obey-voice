package speaker

import (
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
)

// The analyzer's labels must keep the meaning prompts give them: the miss
// label names nobody, anonymous ids name nobody known, and an enrolled name
// is the speaker's own even though the rename table does not hold it.
func TestLiveLabelsKeepTheirPromptMeaning(t *testing.T) {
	names := NewNameMap()
	names.SeedPrimary("Lance")
	if got := brain.PromptSpeaker(LabelUnknown); got != "" {
		t.Fatalf("PromptSpeaker(%q) = %q", LabelUnknown, got)
	}
	if got := brain.FormatUserLine(LabelUnknown, "hi", names); got != "User: hi" {
		t.Fatalf("unidentified line = %q", got)
	}
	for label, want := range map[string]string{
		LabelUnknown:      "Lance",
		AnonymousLabel(1): "Lance",
		AnonymousLabel(2): "Unknown",
		"Maria":           "Maria",
	} {
		if got := brain.HumanSpeakerName(label, names, "Lance"); got != want {
			t.Errorf("HumanSpeakerName(%q) = %q, want %q", label, got, want)
		}
	}
}
