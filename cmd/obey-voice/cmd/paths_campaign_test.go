package cmd

import (
	"encoding/json"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

// TestPathsReportsWorkDirProvenance covers the human rows a user actually reads
// when they are asking "why is the agent working there?".
func TestPathsReportsWorkDirProvenance(t *testing.T) {
	config.SetConfigDirForTest(t, t.TempDir())

	tests := []struct {
		name   string
		report pathsReport
		want   []string
	}{
		{
			name: "an environment variable is named as the source",
			report: pathsReport{
				WorkDir:             "/w",
				WorkDirSource:       string(config.WorkDirSourceEnv),
				WorkDirSourceDetail: config.WorkDirInfo{Source: config.WorkDirSourceEnv, EnvVar: "WORK_DIR"}.Describe(),
				Campaign:            campaignReport{},
			},
			want: []string{"Work dir source", "env", "the WORK_DIR environment variable", "none (work dir is not inside a campaign)"},
		},
		{
			name: "a campaign is named with its root",
			report: pathsReport{
				WorkDir:             "/w",
				WorkDirSource:       string(config.WorkDirSourceLaunchDir),
				WorkDirSourceDetail: config.WorkDirInfo{Source: config.WorkDirSourceLaunchDir}.Describe(),
				Campaign:            campaignReport{Found: true, Name: "My_Tools", ID: "c-1", Root: "/w"},
			},
			want: []string{"launch-dir", "Campaign", "My_Tools (root: /w)"},
		},
		{
			name: "a campaign whose marker could not be read is still a campaign",
			report: pathsReport{
				WorkDir:  "/w",
				Campaign: campaignReport{Found: true, Root: "/w"},
			},
			want: []string{"unnamed (root: /w)"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := runPathsCmd(t, tt.report, false)
			for _, want := range tt.want {
				if !contains(out, want) {
					t.Errorf("paths output missing %q:\n%s", want, out)
				}
			}
		})
	}
}

// The JSON half carries the same facts, with campaign as an always-present
// object so a client can tell "not in a campaign" from "not reported".
func TestPathsJSONCarriesProvenanceAndCampaign(t *testing.T) {
	config.SetConfigDirForTest(t, t.TempDir())
	dir := t.TempDir()
	t.Chdir(dir)

	var got pathsReport
	if err := json.Unmarshal([]byte(runPathsCmd(t, collectPaths(), true)), &got); err != nil {
		t.Fatalf("paths --json is not valid JSON: %v", err)
	}
	if got.WorkDir != dir {
		t.Fatalf("work_dir = %q, want the launch directory %q", got.WorkDir, dir)
	}
	if got.WorkDirSource != string(config.WorkDirSourceLaunchDir) {
		t.Errorf("work_dir_source = %q, want %q", got.WorkDirSource, config.WorkDirSourceLaunchDir)
	}
	if got.WorkDirSourceDetail == "" {
		t.Error("work_dir_source_detail is empty; the human half of the pair must always answer")
	}
	if got.Campaign.Found {
		t.Errorf("campaign = %+v, want none for a temp directory", got.Campaign)
	}
}
