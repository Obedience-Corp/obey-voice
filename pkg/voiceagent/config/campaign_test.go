package config

import (
	"os"
	"path/filepath"
	"testing"
)

// writeCampaignMarker plants .campaign/campaign.yaml under root with the given
// body, returning root for convenience.
func writeCampaignMarker(t *testing.T, root, body string) string {
	t.Helper()
	dir := filepath.Join(root, campaignMarkerDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, campaignMarkerFile), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return root
}

const validMarker = "id: 8a57dff6-753a-41c2-be15-47c3d8fc4ca8\nname: My_Tools\ntype: product\n"

// TestDetectCampaign locks the walk. The failure cases come first: they decide
// what the agent is told when the answer is not clean, and "none" is the
// dangerous answer — it is the one that lets a redirected launch look normal.
func TestDetectCampaign(t *testing.T) {
	deep := t.TempDir()
	deepRoot := writeCampaignMarker(t, deep, validMarker)
	deepStart := filepath.Join(deep, "projects", "obey-voice", "pkg", "voiceagent")
	if err := os.MkdirAll(deepStart, 0o755); err != nil {
		t.Fatal(err)
	}

	outer := writeCampaignMarker(t, t.TempDir(), validMarker)
	inner := filepath.Join(outer, "nested")
	writeCampaignMarker(t, mkdir(t, inner), "name: Inner\nid: inner-1\n")

	malformed := writeCampaignMarker(t, t.TempDir(), "name: [unterminated\n\tid: nope\n")
	empty := writeCampaignMarker(t, t.TempDir(), "")
	extra := writeCampaignMarker(t, t.TempDir(), "name: Extra\nid: x-1\nfuture_key: {a: 1}\nconcepts:\n  - name: projects\n")

	tests := []struct {
		name  string
		start string
		want  Campaign
	}{
		{"no marker anywhere is none", t.TempDir(), Campaign{}},
		{"an empty path is none, never the process cwd", "", Campaign{}},
		{"a path that does not exist walks up and stops at the filesystem root", filepath.Join(t.TempDir(), "a", "b", "c"), Campaign{}},
		{"the filesystem root itself terminates", rootDir, Campaign{}},
		{"a malformed marker is still a campaign, unnamed", malformed, Campaign{Root: malformed}},
		{"an empty marker is still a campaign, unnamed", empty, Campaign{Root: empty}},
		{"unknown fields are ignored", extra, Campaign{Name: "Extra", ID: "x-1", Root: extra}},
		{"marker in the directory itself", deepRoot, Campaign{Name: "My_Tools", ID: "8a57dff6-753a-41c2-be15-47c3d8fc4ca8", Root: deepRoot}},
		{"marker four levels up", deepStart, Campaign{Name: "My_Tools", ID: "8a57dff6-753a-41c2-be15-47c3d8fc4ca8", Root: deepRoot}},
		{"the nearest root wins", inner, Campaign{Name: "Inner", ID: "inner-1", Root: inner}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DetectCampaign(tt.start)
			if got != tt.want {
				t.Errorf("DetectCampaign(%q) = %+v, want %+v", tt.start, got, tt.want)
			}
			if got.Found() != (got.Root != "") {
				t.Errorf("Found() = %v for root %q", got.Found(), got.Root)
			}
		})
	}
}

// An unreadable marker must not demote the campaign to "none": the directory it
// sits in is a campaign root whether or not this process may read the file.
func TestDetectCampaignUnreadableMarkerStaysACampaign(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads any mode")
	}
	root := writeCampaignMarker(t, t.TempDir(), validMarker)
	marker := filepath.Join(root, campaignMarkerDir, campaignMarkerFile)
	if err := os.Chmod(marker, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(marker, 0o600) })

	got := DetectCampaign(root)
	if !got.Found() || got.Root != root {
		t.Fatalf("DetectCampaign = %+v, want the campaign at %q", got, root)
	}
	if got.Name != "" {
		t.Errorf("Name = %q, want empty for an unreadable marker", got.Name)
	}
	if want := "unnamed (root: " + root + ")"; got.Describe() != want {
		t.Errorf("Describe() = %q, want %q", got.Describe(), want)
	}
}

// A directory holding .campaign as a *file*, or a campaign.yaml that is a
// directory, is not a campaign root — the marker must be a regular file, or a
// stray name would ground the agent in a campaign that does not exist.
func TestDetectCampaignRequiresARegularMarkerFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, campaignMarkerDir, campaignMarkerFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := DetectCampaign(dir); got.Found() {
		t.Errorf("DetectCampaign = %+v, want none for a directory marker", got)
	}
}

// The cache is what keeps the per-turn grounding block free. It is documented
// as process-lifetime, so a marker planted after the first answer is not picked
// up — DetectCampaign is the uncached door.
func TestCampaignForCachesPerDirectory(t *testing.T) {
	dir := t.TempDir()
	if got := CampaignFor(dir); got.Found() {
		t.Fatalf("CampaignFor = %+v, want none before the marker exists", got)
	}
	writeCampaignMarker(t, dir, validMarker)

	if got := CampaignFor(dir); got.Found() {
		t.Errorf("CampaignFor = %+v, want the cached miss", got)
	}
	if got := DetectCampaign(dir); !got.Found() || got.Name != "My_Tools" {
		t.Errorf("DetectCampaign = %+v, want the campaign that now exists", got)
	}

	fresh := writeCampaignMarker(t, t.TempDir(), validMarker)
	got := CampaignFor(fresh)
	if !got.Found() || got.Name != "My_Tools" || got.Root != fresh {
		t.Errorf("CampaignFor(%q) = %+v, want the campaign", fresh, got)
	}
	if again := CampaignFor(fresh); again != got {
		t.Errorf("second CampaignFor = %+v, want %+v", again, got)
	}
}

func TestCampaignDescribe(t *testing.T) {
	tests := []struct {
		name string
		c    Campaign
		want string
	}{
		{"none", Campaign{}, "none"},
		{"unnamed", Campaign{Root: "/w"}, "unnamed (root: /w)"},
		{"named", Campaign{Name: "My_Tools", Root: "/w"}, "My_Tools (root: /w)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.c.Describe(); got != tt.want {
				t.Errorf("Describe() = %q, want %q", got, tt.want)
			}
		})
	}
}

func mkdir(t *testing.T, dir string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}
