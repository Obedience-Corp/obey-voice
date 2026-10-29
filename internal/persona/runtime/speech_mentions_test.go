package personaruntime

import (
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
)

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
			if admitted.AgentMentionControl == nil {
				t.Fatal("response has no roster lookup")
			}
			for _, target := range []string{"venessa-agent", "Venessa", "vEe", "alex-one"} {
				if !admitted.AgentMentionControl(target) {
					t.Errorf("registered control %q not recognized", target)
				}
			}
			for _, target := range []string{"support", "typescript", "Alex", "away"} {
				if admitted.AgentMentionControl(target) {
					t.Errorf("unknown, ambiguous or inactive target %q would be removed from speech", target)
				}
			}
			peer.DisplayName, peer.Aliases = "Renamed", nil
			writeTestProfile(t, f.catalog.Dir, peer)
			if !admitted.AgentMentionControl("Venessa") || admitted.AgentMentionControl("Renamed") {
				t.Fatal("an in-flight response changed its roster lookup")
			}
			fresh, err := f.selector.Route(t.Context(), tc.input, tc.background)
			if err != nil {
				t.Fatal(err)
			}
			if fresh.AgentMentionControl == nil || fresh.AgentMentionControl("Venessa") || !fresh.AgentMentionControl("Renamed") {
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
	if admitted.AgentMentionControl == nil || !admitted.AgentMentionControl("Obey") || admitted.AgentMentionControl("support") {
		t.Fatal("handoff did not carry the resolved speech controls")
	}
}
