package meeting

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

func launchCampaignDir(t *testing.T, root, name string) string {
	t.Helper()
	markerDir := filepath.Join(root, ".campaign")
	if err := os.MkdirAll(markerDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(markerDir, "campaign.yaml"), []byte("name: "+name+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestFromConfigLaunchCampaign(t *testing.T) {
	root := launchCampaignDir(t, t.TempDir(), "My_Tools")
	nested := filepath.Join(root, "projects", "worktrees", "obey-voice", "feature")
	if err := os.MkdirAll(nested, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{root, nested} {
		t.Run(dir, func(t *testing.T) {
			cfg := &config.Config{WorkDir: dir}
			got := FromConfig(cfg)
			if got.Default != "camp:My_Tools" || got.Mode != ModeAsk {
				t.Fatalf("routing = %+v", got)
			}
			dest, ok := got.DestinationByID(got.Default)
			if !ok || dest.Campaign != "My_Tools" || dest.CampaignRoot != root || dest.Capture != CaptureMeeting {
				t.Fatalf("destination = %+v, found=%v", dest, ok)
			}
			if cfg.Meeting.Route.Default != "" || len(cfg.Meeting.Route.Destinations) != 0 {
				t.Fatal("session default mutated persisted configuration")
			}
		})
	}

	// An unset work_dir uses the launch cwd, including nested campaigns.
	inner := launchCampaignDir(t, filepath.Join(root, "nested"), "Inner")
	t.Chdir(inner)
	t.Setenv("WORK_DIR", "")
	t.Setenv("OBEY_VOICE_WORK_DIR", "")
	if got := FromConfig(&config.Config{}); got.Default != "camp:Inner" {
		t.Fatalf("launch cwd default = %q", got.Default)
	}
	if got := FromConfig(&config.Config{WorkDir: root}); got.Default != "camp:My_Tools" {
		t.Fatalf("explicit working directory default = %q", got.Default)
	}
}

func TestFromConfigLaunchCampaignPrecedence(t *testing.T) {
	root := launchCampaignDir(t, t.TempDir(), "My_Tools")
	unnamed := launchCampaignDir(t, t.TempDir(), "")
	for _, tc := range []struct {
		name  string
		dir   string
		route config.MeetingRouteConfig
		want  string
		count int
	}{
		{name: "outside campaign", dir: t.TempDir()},
		{name: "unnamed campaign", dir: unnamed, want: CampaignDestinationID(unnamed), count: 1},
		{name: "routing off", dir: root, route: config.MeetingRouteConfig{Mode: ModeOff}},
		{name: "explicit default", dir: root, route: config.MeetingRouteConfig{Default: "docs"}, want: "docs"},
		{name: "auto default", dir: root, route: config.MeetingRouteConfig{Mode: ModeAuto}, want: "camp:My_Tools", count: 1},
		{name: "configured campaign alias", dir: root, route: config.MeetingRouteConfig{Destinations: []config.MeetingDestinationConfig{
			{ID: "my-notes", Type: TypeCampaign, Campaign: "My_Tools", Capture: CaptureNote, Tags: []string{"team"}},
		}}, want: "my-notes", count: 1},
		{name: "configured campaign path", dir: root, route: config.MeetingRouteConfig{Destinations: []config.MeetingDestinationConfig{
			{ID: "local-campaign", Type: TypeCampaign, Campaign: root},
		}}, want: "local-campaign", count: 1},
		{name: "conflicting destination id", dir: root, route: config.MeetingRouteConfig{Destinations: []config.MeetingDestinationConfig{
			{ID: "camp:My_Tools", Type: TypeFile, Path: t.TempDir()},
		}}, count: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.Config{WorkDir: tc.dir, Meeting: config.MeetingConfig{Route: tc.route}}
			got := FromConfig(cfg)
			if got.Default != tc.want || len(got.Destinations) != tc.count {
				t.Fatalf("routing = %+v, want default %q and %d destinations", got, tc.want, tc.count)
			}
			if !reflect.DeepEqual(cfg.Meeting.Route, tc.route) {
				t.Fatal("configuration changed")
			}
			if tc.name == "configured campaign alias" {
				dest, _ := got.DestinationByID(tc.want)
				if dest.Capture != CaptureNote || !reflect.DeepEqual(dest.Tags, []string{"team"}) {
					t.Fatalf("configured capture/tags changed: %+v", dest)
				}
			}
		})
	}
}

func TestLaunchCampaignSurvivesRegistryFailure(t *testing.T) {
	root := launchCampaignDir(t, t.TempDir(), "Local")
	router := &Router{
		Cfg:      FromConfig(&config.Config{WorkDir: root}),
		LookPath: func(string) (string, error) { return "camp", nil },
		Run: func(context.Context, string, ...string) ([]byte, error) {
			return nil, errors.New("registry unavailable")
		},
	}
	expanded, dests, err := router.ExpandForRouting(context.Background())
	if err == nil || len(dests) != 1 || expanded.Default != "camp:Local" {
		t.Fatalf("expand = %+v, %+v, %v", expanded, dests, err)
	}
	sink, err := router.sinkFor(dests[0])
	if err != nil {
		t.Fatal(err)
	}
	dir, err := sink.(CampaignSink).campaignDir(context.Background(), "Local")
	if err != nil || dir != root {
		t.Fatalf("delivery directory = %q, %v; want %q", dir, err, root)
	}

	router.LookPath = func(string) (string, error) { return "", os.ErrNotExist }
	if available := router.AvailableDestinations(); len(available) != 0 {
		t.Fatalf("campaign offered without camp: %+v", available)
	}
}
