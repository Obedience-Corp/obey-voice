package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

func TestFormatNoticePrettyPrintsJSON(t *testing.T) {
	got := formatNotice(`Meeting notes routed to camp:JobSearch: {"campaign_root":"/tmp/JobSearch","generated_at":"2026-09-18T17:19:25Z"}`, 80, false)
	plain := stripANSI(got)
	if !strings.Contains(plain, "Meeting notes routed to camp:JobSearch") {
		t.Fatalf("title missing:\n%s", plain)
	}
	if !strings.HasPrefix(strings.TrimLeft(plain, "\n"), "  Meeting notes") {
		t.Fatalf("missing gutter:\n%s", plain)
	}
	if strings.Count(plain, "\n") < 2 || !strings.Contains(plain, `"campaign_root"`) {
		t.Fatalf("JSON not indented:\n%s", plain)
	}
	if !strings.Contains(got, "\x1b[") {
		t.Fatal("JSON has no color")
	}
}

func TestFormatNoticeSplitsPath(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, ".obey", "meetings", "call.meeting", "speaker-analysis.json")
	got := stripANSI(formatNotice("Speaker labels updated: 7 speakers · "+path, 80, false))
	if !strings.Contains(got, "Speaker labels updated: 7 speakers") {
		t.Fatalf("title missing:\n%s", got)
	}
	if !strings.Contains(got, "speaker-analysis.json") {
		t.Fatalf("basename missing:\n%s", got)
	}
	if strings.Contains(got, home) {
		t.Fatalf("home not shortened:\n%s", got)
	}
	if strings.Contains(got, "7 speakers · "+path) {
		t.Fatal("raw path still inlined")
	}
}

func TestFormatNoticeCompactDropsJSONBody(t *testing.T) {
	got := stripANSI(formatNoticeCompact(`Meeting notes routed to camp:JobSearch: {"campaign_root":"/tmp"}`, 60, false))
	if strings.Contains(got, "campaign_root") {
		t.Fatalf("compact kept JSON:\n%s", got)
	}
	if !strings.Contains(got, "camp:JobSearch") {
		t.Fatalf("compact lost title:\n%s", got)
	}
}

func TestShortenHomeIsPathBounded(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	if got := shortenHome(home + "-work" + string(filepath.Separator) + "x"); strings.Contains(got, "~") {
		t.Fatalf("replaced a prefix of a longer name: %q", got)
	}
	if got := shortenHome("/opt" + home + string(filepath.Separator) + "x"); strings.Contains(got, "~") {
		t.Fatalf("replaced a non-boundary substring: %q", got)
	}
	want := "~" + string(filepath.Separator) + "Dev"
	if got := shortenHome(filepath.Join(home, "Dev")); got != want {
		t.Fatalf("path prefix = %q, want %q", got, want)
	}
}

func TestLauncherBannerRendersPrettyJSON(t *testing.T) {
	m := newLauncher(&config.Config{BrainProvider: "ollama"}, nil)
	m.width, m.height = 100, 28
	m = m.withBanner(`Meeting notes routed to camp:JobSearch: {"campaign_root":"/tmp/JobSearch"}`, false)
	plain := stripANSI(m.View())
	if !strings.Contains(plain, `"campaign_root"`) || !strings.Contains(plain, "Meeting notes routed") {
		t.Fatalf("launcher banner:\n%s", plain)
	}
	if strings.Contains(strings.Split(plain, "New conversation")[0], `{"campaign_root"`) {
		t.Fatal("raw JSON object still on one title line")
	}
}
