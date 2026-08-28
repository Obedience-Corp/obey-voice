package netapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

// GET /v1/status carries the same work-dir facts as the ready banner. A client
// that polls status instead of reading the launch line must not have to guess
// whether an environment variable moved the agent, or which campaign it is
// acting on — the two surfaces answer that question identically or neither can
// be trusted.
func TestStatusCarriesWorkDirSourceAndCampaign(t *testing.T) {
	campaign := CampaignInfo{Found: true, Name: "My_Tools", ID: "c-1", Root: "/w"}
	s := New(Options{
		Credentials:   &Credentials{Fingerprint: "fp"},
		Dispatcher:    NewDispatcher(&scriptedRunner{}, events.NewBus(), nil, nil),
		WorkDir:       "/w/projects/obey-voice",
		WorkDirSource: "env",
		Campaign:      campaign,
	})

	rec := httptest.NewRecorder()
	req, err := http.NewRequest(http.MethodGet, "https://example/v1/status", nil)
	if err != nil {
		t.Fatal(err)
	}
	s.handleStatus(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body struct {
		WorkDir       string       `json:"work_dir"`
		WorkDirSource string       `json:"work_dir_source"`
		Campaign      CampaignInfo `json:"campaign"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode status: %v\n%s", err, rec.Body.String())
	}
	if body.WorkDir != "/w/projects/obey-voice" {
		t.Errorf("work_dir = %q, want the configured directory", body.WorkDir)
	}
	if body.WorkDirSource != "env" {
		t.Errorf("work_dir_source = %q, want %q", body.WorkDirSource, "env")
	}
	if body.Campaign != campaign {
		t.Errorf("campaign = %+v, want %+v", body.Campaign, campaign)
	}

	// Both keys are unconditional, like work_dir: absence must not be how a
	// client learns "no campaign".
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &keys); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"work_dir", "work_dir_source", "campaign"} {
		if _, ok := keys[key]; !ok {
			t.Errorf("%s missing from /v1/status: %s", key, rec.Body.String())
		}
	}
}
