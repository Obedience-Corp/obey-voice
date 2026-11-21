package brain

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

// campaignRoot plants .campaign/campaign.yaml in a fresh directory.
func campaignRoot(t *testing.T, body string) string {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, ".campaign")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "campaign.yaml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return root
}

// TestEnvironmentContextNamesTheCampaign is the whole point of the grounding
// change: the model is told which workspace it is standing in, so a working
// directory that was redirected out from under it stops looking normal.
func TestEnvironmentContextNamesTheCampaign(t *testing.T) {
	root := campaignRoot(t, "id: c-1\nname: My_Tools\n")
	nested := filepath.Join(root, "projects", "obey-voice")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	unnamed := campaignRoot(t, "type: product\n")

	tests := []struct {
		name    string
		workDir string
		want    string
	}{
		{"outside a campaign", t.TempDir(), "- Campaign: none — the working directory is not inside a campaign"},
		{"an unknown work dir claims nothing", "", "- Campaign: none — the working directory is not inside a campaign"},
		{"at the campaign root", root, "- Campaign: My_Tools (root: " + root + ")"},
		{"inside a campaign project", nested, "- Campaign: My_Tools (root: " + root + ")"},
		{"a campaign whose marker has no name", unnamed, "- Campaign: unnamed (root: " + unnamed + ")"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := EnvironmentContextFrom(tt.workDir, Env{User: "lance", Hostname: "host", OS: "darwin/arm64"})
			if !strings.Contains(got, tt.want) {
				t.Errorf("grounding block missing %q:\n%s", tt.want, got)
			}
			// The campaign line belongs with the directory it describes.
			if !strings.Contains(got, "- Working directory: "+tt.workDir+"\n- Campaign: ") {
				t.Errorf("campaign line is not next to the working directory:\n%s", got)
			}
			if strings.Contains(got, "web_search") {
				t.Errorf("environment grounding must not advertise worker tools:\n%s", got)
			}
		})
	}
}

func TestEveryProviderIsToldWhatAtPathsAreRelativeTo(t *testing.T) {
	root := campaignRoot(t, "id: c-1\nname: My_Tools\n")
	launch := filepath.Join(root, "projects", "app")
	if err := os.MkdirAll(launch, 0o755); err != nil {
		t.Fatal(err)
	}
	pinned := t.TempDir()
	cfg := &config.Config{
		EnvironmentContextEnabled: true,
		PromptsDir:                t.TempDir(),
		ConversationDirectories:   &config.ConversationDirectories{Default: config.ConversationDirectory{Dir: launch, Source: "launch-dir"}},
	}
	want := "- All file paths are relative to the working directory unless absolute\n- An @path in the user's message, such as @projects/app/ or @./README.md, names a file or directory relative to " + root + "; it is a path, not an agent address.\n"
	for _, provider := range []string{providerOllama, providerClaude, providerCursor, providerGrok, "codex", "muse-spark", "obey"} {
		got := AssembleSystemPrompt(SystemPromptInput{Provider: provider, Persona: "P", WorkDir: pinned, Cfg: cfg})
		if !strings.Contains(got, want) || strings.Count(got, "An @path") != 1 {
			t.Fatalf("%s: missing the @path line next to the path rule:\n%s", provider, got)
		}
	}
	cfg.EnvironmentContextEnabled = false
	if got := AssembleSystemPrompt(SystemPromptInput{Provider: providerOllama, Persona: "P", WorkDir: pinned, Cfg: cfg}); strings.Contains(got, "An @path") {
		t.Fatalf("@path line without the environment block:\n%s", got)
	}
}
