//go:build !integration

package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Obedience-Corp/obey-voice/internal/netapi"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

// serveCampaignDir plants a campaign root serve can be pointed at.
func serveCampaignDir(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".campaign"), 0o755); err != nil {
		t.Fatal(err)
	}
	body := []byte("id: c-1\nname: My_Tools\n")
	if err := os.WriteFile(filepath.Join(root, ".campaign", "campaign.yaml"), body, 0o600); err != nil {
		t.Fatal(err)
	}
	return root
}

// A supervising app reads the ready line, so provenance and campaign have to be
// on it: "where do tool calls land" and "whose workspace is that" are the two
// questions a hijacked launch answers wrongly without anyone noticing.
func TestReadyBannerCarriesWorkDirSourceAndCampaign(t *testing.T) {
	withServeBindState(t)
	orig := serveWorkDirInfo
	t.Cleanup(func() { serveWorkDirInfo = orig })

	root := serveCampaignDir(t)
	serveWorkDirInfo = config.WorkDirInfo{Dir: root, Source: config.WorkDirSourceEnv, EnvVar: "WORK_DIR"}

	raw := captureBannerLine(t, "127.0.0.1:7262", []string{"127.0.0.1:7262"})
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(raw, &keys); err != nil {
		t.Fatalf("decode banner: %v", err)
	}
	for _, key := range []string{"work_dir", "work_dir_source", "campaign"} {
		if _, ok := keys[key]; !ok {
			t.Fatalf("%s missing from the ready line: %s", key, raw)
		}
	}

	var banner netapi.ReadyBanner
	if err := json.Unmarshal(raw, &banner); err != nil {
		t.Fatalf("decode ready banner: %v", err)
	}
	if banner.WorkDirSource != string(config.WorkDirSourceEnv) {
		t.Errorf("work_dir_source = %q, want %q", banner.WorkDirSource, config.WorkDirSourceEnv)
	}
	want := netapi.CampaignInfo{Found: true, Name: "My_Tools", ID: "c-1", Root: root}
	if banner.Campaign != want {
		t.Errorf("campaign = %+v, want %+v", banner.Campaign, want)
	}
}

// The human card states the same two facts, and a source nobody typed is not
// printed dim: an environment variable deciding the work dir is the case this
// banner exists to surface.
func TestServeHumanBannerStatesWorkDirAndCampaign(t *testing.T) {
	withServeBindState(t)
	orig := serveWorkDirInfo
	t.Cleanup(func() { serveWorkDirInfo = orig })

	root := serveCampaignDir(t)
	var human bytes.Buffer
	origHuman := serveHumanOut
	serveHumanOut = &human
	t.Cleanup(func() { serveHumanOut = origHuman })

	serveWorkDirInfo = config.WorkDirInfo{Dir: root, Source: config.WorkDirSourceEnv, EnvVar: "OBEY_VOICE_WORK_DIR"}
	printServeWorkDir()

	out := human.String()
	for _, want := range []string{
		"Work dir: " + root,
		"the OBEY_VOICE_WORK_DIR environment variable",
		"Campaign: My_Tools (root: " + root + ")",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("serve banner missing %q:\n%s", want, out)
		}
	}
}
