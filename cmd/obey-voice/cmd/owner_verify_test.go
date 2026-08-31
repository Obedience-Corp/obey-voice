//go:build !integration

package cmd

import (
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/internal/speaker"
)

func TestOwnerVerifyGate(t *testing.T) {
	cases := []struct {
		name       string
		st         speaker.LiveStats
		wantReject bool
	}{
		{"disabled fails open", speaker.LiveStats{Status: speaker.LiveDisabled, LastLabel: "speaker-2"}, false},
		{"unavailable fails open", speaker.LiveStats{Status: speaker.LiveUnavailable, LastLabel: "speaker-2"}, false},
		{"degraded fails open", speaker.LiveStats{Status: speaker.LiveDegraded, LastLabel: "speaker-2"}, false},
		{"no label yet fails open", speaker.LiveStats{Status: speaker.LiveHealthy}, false},
		{"enrolled owner allowed", speaker.LiveStats{Status: speaker.LiveHealthy, LastLabel: "lance", LastEnrollRev: "titanet"}, false},
		{"anonymous speaker rejected", speaker.LiveStats{Status: speaker.LiveHealthy, LastLabel: "speaker-2"}, true},
		{"running anonymous rejected", speaker.LiveStats{Status: speaker.LiveRunning, LastLabel: "speaker-2"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gate := ownerVerifyGate(func() speaker.LiveStats { return tc.st })
			reason := gate(tc.st.LastLabel)
			if got := reason != ""; got != tc.wantReject {
				t.Fatalf("gate(%+v) = %q, wantReject=%v", tc.st, reason, tc.wantReject)
			}
		})
	}
}

// TestBargeInSpeakerVerdict proves bargeInSpeakerVerdict maps
// speaker.LiveStats onto pipeline.SpeakerVerdict using the same
// ConfidentlyHealthy/LastEnrollRev checks as ownerVerifyGate above — the two
// privacy-guard consumers must never disagree about "healthy" or "enrolled".
func TestBargeInSpeakerVerdict(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name         string
		st           speaker.LiveStats
		wantHealthy  bool
		wantVerdict  bool
		wantEnrolled bool
	}{
		{"disabled: unhealthy", speaker.LiveStats{Status: speaker.LiveDisabled, LastLabel: "speaker-2"}, false, true, false},
		{"unavailable: unhealthy", speaker.LiveStats{Status: speaker.LiveUnavailable, LastLabel: "speaker-2"}, false, true, false},
		{"degraded: unhealthy", speaker.LiveStats{Status: speaker.LiveDegraded, LastLabel: "speaker-2"}, false, true, false},
		{"healthy, no label yet: no verdict", speaker.LiveStats{Status: speaker.LiveHealthy}, true, false, false},
		{"healthy + enrolled owner", speaker.LiveStats{Status: speaker.LiveHealthy, LastLabel: "lance", LastEnrollRev: "titanet", LastUpdated: now}, true, true, true},
		{"healthy + anonymous stranger", speaker.LiveStats{Status: speaker.LiveHealthy, LastLabel: "speaker-2", LastUpdated: now}, true, true, false},
		{"running + anonymous stranger", speaker.LiveStats{Status: speaker.LiveRunning, LastLabel: "speaker-2", LastUpdated: now}, true, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			verdict := bargeInSpeakerVerdict(func() speaker.LiveStats { return tc.st })()
			if verdict.Healthy != tc.wantHealthy {
				t.Errorf("Healthy = %v, want %v", verdict.Healthy, tc.wantHealthy)
			}
			if verdict.HasVerdict != tc.wantVerdict {
				t.Errorf("HasVerdict = %v, want %v", verdict.HasVerdict, tc.wantVerdict)
			}
			if verdict.Enrolled != tc.wantEnrolled {
				t.Errorf("Enrolled = %v, want %v", verdict.Enrolled, tc.wantEnrolled)
			}
			if verdict.Label != tc.st.LastLabel {
				t.Errorf("Label = %q, want %q", verdict.Label, tc.st.LastLabel)
			}
			if !verdict.At.Equal(tc.st.LastUpdated) {
				t.Errorf("At = %v, want %v", verdict.At, tc.st.LastUpdated)
			}
		})
	}
}
