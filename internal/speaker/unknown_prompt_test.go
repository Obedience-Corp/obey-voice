package speaker

import (
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
)

// The live analyzer's miss label must stay the one prompts treat as nobody.
func TestUnknownLabelNeverNamesAPromptSpeaker(t *testing.T) {
	names := NewNameMap()
	names.SeedPrimary("Lance")
	if got := brain.PromptSpeaker(LabelUnknown); got != "" {
		t.Fatalf("PromptSpeaker(%q) = %q", LabelUnknown, got)
	}
	if got := brain.FormatUserLine(LabelUnknown, "hi", names); got != "User: hi" {
		t.Fatalf("unidentified line = %q", got)
	}
	if got := brain.FormatUserLine(AnonymousLabel(1), "hi", names); got != "Lance: hi" {
		t.Fatalf("seeded owner line = %q", got)
	}
}
