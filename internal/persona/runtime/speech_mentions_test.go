package personaruntime

import (
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/pipeline"
)

// spokenMention is the name speech gives an @handle in this response, or ""
// when the handle is not an agent control and is spoken as written.
func spokenMention(admitted pipeline.ResponseAdmission, target string) string {
	if admitted.AgentMentionName == nil {
		return ""
	}
	name, _ := admitted.AgentMentionName(target)
	return name
}

func TestResponseSpeechControlsUseFrozenRoster(t *testing.T) {
	for _, tc := range []struct {
		name, input string
		background  bool
	}{
		{"ordinary", "Tell me more.", false},
		{"exact", "@obey Tell me more.", false},
		{"background", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			peer := testProfile("venessa-agent", "Venessa", "venessa")
			peer.Aliases = []string{"Vee"}
			inactive := testProfile("away", "Away", "away")
			inactive.Inactive = true
			f := newSelectorTestFixture(t, testProfile("obey", "Obey", "obey"), peer,
				testProfile("alex-one", "Alex One", "alex-one"), testProfile("alex-two", "Alex Two", "alex-two"), inactive)
			defer f.close(t)
			admitted, err := f.selector.Route(t.Context(), tc.input, tc.background)
			if err != nil {
				t.Fatal(err)
			}
			if admitted.AgentMentionName == nil {
				t.Fatal("response has no roster lookup")
			}
			// An ID, a name in any case and an alias are all said as the
			// persona's display name.
			for target, want := range map[string]string{"venessa-agent": "Venessa", "Venessa": "Venessa", "vEe": "Venessa", "alex-one": "Alex One"} {
				if got := spokenMention(admitted, target); got != want {
					t.Errorf("registered control %q spoken as %q, want %q", target, got, want)
				}
			}
			for _, target := range []string{"support", "typescript", "Alex", "away"} {
				if got := spokenMention(admitted, target); got != "" {
					t.Errorf("unknown, ambiguous or inactive target %q would be spoken as %q", target, got)
				}
			}
			peer.DisplayName, peer.Aliases = "Renamed", nil
			writeTestProfile(t, f.catalog.Dir, peer)
			if spokenMention(admitted, "Venessa") != "Venessa" || spokenMention(admitted, "Renamed") != "" {
				t.Fatal("an in-flight response changed its roster lookup")
			}
			fresh, err := f.selector.Route(t.Context(), tc.input, tc.background)
			if err != nil {
				t.Fatal(err)
			}
			if spokenMention(fresh, "Venessa") != "" || spokenMention(fresh, "Renamed") != "Renamed" {
				t.Fatal("next response did not refresh its roster lookup")
			}
		})
	}
}

func TestHandoffAdmissionCarriesSpeechControls(t *testing.T) {
	f := newSelectorTestFixture(t, testProfile("obey", "Obey", "obey"), testProfile("venessa-agent", "Venessa", "venessa"))
	defer f.close(t)
	proposal, accepted, err := f.selector.RouteHandoff(t.Context(), brain.Turn{Role: "assistant", PersonaID: "obey", Content: "@Venessa Hello"}, nil)
	if err != nil || !accepted {
		t.Fatalf("accepted=%v err=%v", accepted, err)
	}
	admitted, err := proposal.Commit(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if spokenMention(admitted, "Obey") != "Obey" || spokenMention(admitted, "support") != "" {
		t.Fatal("handoff did not carry the resolved speech controls")
	}
}
