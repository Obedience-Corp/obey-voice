package config

import (
	"strings"
	"testing"
)

// TestDiagnoseWorkDirSource pins the severities. env and the fallbacks are the
// silent hijacks — nothing in config.yaml explains them — so they warn; the
// sources a user typed report OK, because a row that only appears when
// something is wrong teaches nobody where to look.
func TestDiagnoseWorkDirSource(t *testing.T) {
	tests := []struct {
		name string
		// setup returns the config to diagnose; the wanted directory is the
		// second result so each case can build its own.
		setup      func(t *testing.T, env workDirEnv) (*Config, string)
		want       Severity
		wantDetail []string
	}{
		{
			name: "a bare WORK_DIR warns and names itself",
			setup: func(t *testing.T, _ workDirEnv) (*Config, string) {
				dir := t.TempDir()
				t.Setenv("WORK_DIR", dir)
				return loadForTest(t), dir
			},
			want:       SeverityWarn,
			wantDetail: []string{"WORK_DIR"},
		},
		{
			name: "the documented override warns under its own name",
			setup: func(t *testing.T, _ workDirEnv) (*Config, string) {
				dir := t.TempDir()
				t.Setenv("OBEY_VOICE_WORK_DIR", dir)
				return loadForTest(t), dir
			},
			want:       SeverityWarn,
			wantDetail: []string{"OBEY_VOICE_WORK_DIR"},
		},
		{
			name: "a fallback warns",
			setup: func(t *testing.T, env workDirEnv) (*Config, string) {
				return &Config{WorkDir: "/does/not/exist"}, env.home
			},
			want:       SeverityWarn,
			wantDetail: []string{"home-directory fallback"},
		},
		{
			name: "the setting is OK",
			setup: func(t *testing.T, _ workDirEnv) (*Config, string) {
				dir := t.TempDir()
				return &Config{WorkDir: dir}, dir
			},
			want:       SeverityOK,
			wantDetail: []string{"work_dir setting"},
		},
		{
			name: "the launch directory is OK",
			setup: func(t *testing.T, _ workDirEnv) (*Config, string) {
				dir := t.TempDir()
				t.Chdir(dir)
				return nil, dir
			},
			want:       SeverityOK,
			wantDetail: []string{"launched from"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newWorkDirEnv(t)
			cfg, wantDir := tt.setup(t, env)

			got := diagnoseWorkDirSource(cfg)
			if got.Name != "work-dir-source" {
				t.Errorf("Name = %q, want work-dir-source", got.Name)
			}
			if got.Severity != tt.want {
				t.Errorf("Severity = %q, want %q (%s)", got.Severity, tt.want, got.Detail)
			}
			if !strings.Contains(got.Detail, wantDir) {
				t.Errorf("Detail %q does not name the work dir %q", got.Detail, wantDir)
			}
			for _, want := range tt.wantDetail {
				if !strings.Contains(got.Detail, want) {
					t.Errorf("Detail %q does not mention %q", got.Detail, want)
				}
			}
			if tt.want == SeverityWarn && got.Remediation == "" {
				t.Error("a warning without a remediation tells the user nothing to do")
			}
		})
	}
}

// The row rides Diagnose itself, or nothing prints it.
func TestDiagnoseReportsWorkDirSource(t *testing.T) {
	env := newWorkDirEnv(t)
	dir := t.TempDir()
	t.Setenv("WORK_DIR", dir)
	cfg := healthyBrain(loadForTest(t))

	byName := diagByName(Diagnose(cfg, env.home, okLookPath))
	row, ok := byName["work-dir-source"]
	if !ok {
		t.Fatal("doctor does not report work-dir-source")
	}
	if row.Severity != SeverityWarn || !strings.Contains(row.Detail, "WORK_DIR") {
		t.Errorf("work-dir-source = %+v, want a warning naming WORK_DIR", row)
	}
}

// A campaign under the work dir is named in the same row: "which folder" and
// "whose workspace" are one question when the wrong answer is silent.
func TestDiagnoseWorkDirSourceNamesTheCampaign(t *testing.T) {
	newWorkDirEnv(t)
	dir := writeCampaignMarker(t, t.TempDir(), validMarker)

	got := diagnoseWorkDirSource(&Config{WorkDir: dir})
	if !strings.Contains(got.Detail, "campaign My_Tools") {
		t.Errorf("Detail %q does not name the campaign", got.Detail)
	}
}
