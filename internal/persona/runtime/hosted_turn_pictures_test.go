package personaruntime

import (
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/chatmedia"
)

// A hosted provider is sent a user line without any of its pictures: those
// the user attached, those a persona opened and those posted before it.
func TestHostedTurnCarriesNoPictures(t *testing.T) {
	ref := chatmedia.Ref{ID: "a1", Name: "chart.png"}
	got, ok := hostedTurn(brain.Turn{
		Role:         "user",
		Content:      "What colour is it?",
		Attachments:  []chatmedia.Ref{ref},
		OpenedImages: []chatmedia.Ref{ref},
		PostedImages: []brain.PostedImage{{Ref: ref, PersonaID: "obey"}},
	})
	if !ok || got.Content != "What colour is it?" {
		t.Fatalf("hosted turn = %+v, %v", got, ok)
	}
	if len(got.Attachments)+len(got.OpenedImages)+len(got.PostedImages) != 0 {
		t.Fatalf("hosted turn keeps pictures: %+v", got)
	}
}
