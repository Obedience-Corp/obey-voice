package meeting

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	meetinglog "github.com/Obedience-Corp/obey-voice/internal/meeting/log"
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

func TestLaunchCampaignLegacyCaptureDelivery(t *testing.T) {
	// Exercise FromConfig -> RouteByID -> the camp subprocess. The fixture
	// models name-based routing through a missing or stale registry, and writes
	// the supplied body into the chosen campaign so we verify delivery itself.
	const fakeCamp = `#!/bin/sh
if [ "$1" != idea ] || [ "$2" != add ]; then
  exit 1 # older camp without import-meeting support
fi
shift 3
target="$PWD"
capture=intent
tags=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    --body-file) body="$2"; shift 2 ;;
    -c) target="$TEST_STALE_CAMPAIGN_ROOT"; shift 2 ;;
    --note) capture=note; shift ;;
    --tag) tags="$tags $2"; shift 2 ;;
    *) exit 2 ;;
  esac
done
[ -n "$target" ] || exit 3
cp "$body" "$target/delivered.md" || exit 4
printf '%s|%s' "$capture" "$tags"
`
	for _, registry := range []string{"missing", "stale"} {
		for _, capture := range []string{CaptureNote, CaptureIntent} {
			for _, withBundle := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/bundle=%t", registry, capture, withBundle), func(t *testing.T) {
					root := launchCampaignDir(t, t.TempDir(), "Local")
					stale := launchCampaignDir(t, t.TempDir(), "Local")
					registryRoot := ""
					if registry == "stale" {
						registryRoot = stale
					}
					t.Setenv("TEST_STALE_CAMPAIGN_ROOT", registryRoot)
					campBin := filepath.Join(t.TempDir(), "camp")
					if err := os.WriteFile(campBin, []byte(fakeCamp), 0o700); err != nil {
						t.Fatal(err)
					}
					cfg := &config.Config{WorkDir: root, Meeting: config.MeetingConfig{Route: config.MeetingRouteConfig{
						Destinations: []config.MeetingDestinationConfig{{ID: "local-notes", Type: TypeCampaign,
							Campaign: "Local", Capture: capture, Tags: []string{"team", "follow up"}}},
					}}}
					router := NewDefaultRouter(FromConfig(cfg))
					router.LookPath = func(string) (string, error) { return campBin, nil }
					note := RenderedNote{Title: "Launch meeting", Body: "# Notes\nKeep the transcript here.\n"}
					if withBundle {
						note.Summary = meetinglog.Summary{Bundle: t.TempDir()}
					}
					receipt, err := router.RouteByID(context.Background(), note, router.Cfg.Default)
					if err != nil || receipt.Outcome != OutcomeRouted {
						t.Fatalf("delivery = %+v, %v", receipt, err)
					}
					if receipt.Detail != capture+"| team follow up" {
						t.Fatalf("capture/tags changed: %q", receipt.Detail)
					}
					body, err := os.ReadFile(filepath.Join(root, "delivered.md"))
					if err != nil || string(body) != note.Body {
						t.Fatalf("delivery in launch root = %q, %v", body, err)
					}
					if _, err := os.Stat(filepath.Join(stale, "delivered.md")); !os.IsNotExist(err) {
						t.Fatalf("stale campaign received delivery: %v", err)
					}
				})
			}
		}
	}
}

func TestLegacyCaptureExplicitDefaultKeepsCampaignSelector(t *testing.T) {
	root := launchCampaignDir(t, t.TempDir(), "Local")
	cfg := &config.Config{WorkDir: root, Meeting: config.MeetingConfig{Route: config.MeetingRouteConfig{
		Default: "chosen", Destinations: []config.MeetingDestinationConfig{
			{ID: "chosen", Type: TypeCampaign, Campaign: "Local", Capture: CaptureNote},
		},
	}}}
	router := NewDefaultRouter(FromConfig(cfg))
	router.LookPath = func(string) (string, error) { return "camp", nil }
	var called bool
	router.Run = func(_ context.Context, _ string, args ...string) ([]byte, error) {
		called = true
		if !strings.Contains(strings.Join(args, " "), "-c Local --note") {
			t.Fatalf("explicit campaign selector lost: %v", args)
		}
		return []byte("saved"), nil
	}
	if _, err := router.RouteByID(context.Background(), RenderedNote{Title: "Explicit", Body: "notes"}, "chosen"); err != nil || !called {
		t.Fatalf("explicit route: called=%v err=%v", called, err)
	}
}
