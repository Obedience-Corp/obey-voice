package cmd

import (
	"encoding/json"
	"fmt"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

var pathsJSON bool

// pathsSchemaVersion identifies the pathsReport shape. Bump only on a
// breaking change (renamed/removed key); adding keys is compatible.
const pathsSchemaVersion = 1

// pathsReport is the machine-readable contract behind `obey-voice paths --json`.
// External supervisors — the Obey Voice Mac app in particular (WI-afaf92) —
// parse it to discover this install's root and serve credentials instead of
// hardcoding the AppSlug. Every key is consumed: add fields freely, but never
// rename or remove one without bumping pathsSchemaVersion.
type pathsReport struct {
	SchemaVersion int    `json:"schema_version"`
	AppSlug       string `json:"app_slug"`
	InstallRoot   string `json:"install_root"`
	ConfigFile    string `json:"config_file"`
	ServeDir      string `json:"serve_dir"`
	CacheDir      string `json:"cache_dir"`
	ModelsDir     string `json:"models_dir"`
	SessionsDir   string `json:"sessions_dir"`
	CapturesDir   string `json:"captures_dir"`
	MeetingsDir   string `json:"meetings_dir"`
	PersonasDir   string `json:"personas_dir"`
	PromptsDir    string `json:"prompts_dir"`
	SkillsDir     string `json:"skills_dir"`
	WorkDir       string `json:"work_dir"`
	// WorkDirSource is the layer that decided WorkDir (flag, config, env,
	// launch-dir, home-fallback, temp-fallback) and WorkDirSourceDetail is the
	// same fact in words, naming the environment variable when one decided it.
	// Both are always present: a client that only sees the path cannot tell a
	// chosen directory from a hijacked one.
	WorkDirSource       string `json:"work_dir_source"`
	WorkDirSourceDetail string `json:"work_dir_source_detail"`
	// Campaign is the campaign workspace WorkDir sits in. Always present, with
	// found=false rather than an omitted key, so "not in a campaign" and "this
	// build does not report campaigns" stay distinguishable.
	Campaign campaignReport `json:"campaign"`
}

// campaignReport is the campaign half of the paths contract.
type campaignReport struct {
	Found bool   `json:"found"`
	Name  string `json:"name"`
	ID    string `json:"id"`
	Root  string `json:"root"`
}

var pathsCmd = &cobra.Command{
	Use:   "paths",
	Short: "Show this install's effective directories and files (read-only)",
	Long: "Show where this install reads and writes: config, serve credentials, model\n" +
		"cache, and session data. Values are effective — config-file and environment\n" +
		"overrides (models_dir/MODELS_DIR, prompts_dir, skills_dir, meeting.dir) are\n" +
		"already applied. work_dir is where the agent's file and shell tools start —\n" +
		"a default, not a sandbox — reported with the layer that decided it and the\n" +
		"campaign it lands in, so a launch redirected by an environment variable is\n" +
		"visible rather than silent.\n\n" +
		"--json emits a single stable object for machine consumers; the Obey Voice\n" +
		"app discovers the install root this way. Keys are a compatibility contract:\n" +
		"only added, never renamed, without a schema_version bump.",
	Args: cobra.NoArgs,
	// Overrides the root hook on purpose: discovery must stay side-effect
	// free (no prompt seeding, no persona migration) and must still answer
	// on installs whose config or persona state would fail Load. On a load
	// failure the built-in defaults are reported, with a warning on stderr
	// so --json stdout stays a single clean object.
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		if _, err := config.LoadRaw(); err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "warning: config load failed, reporting default paths: %v\n", err)
		}
		return nil
	},
	RunE: func(cmd *cobra.Command, args []string) error {
		return runPaths(cmd, collectPaths(), pathsJSON)
	},
}

// collectPaths derives the report exclusively from the centralized config
// helpers so a slug or layout change lands here automatically.
func collectPaths() pathsReport {
	root := config.ConfigDir()
	workDir := config.CurrentWorkDirInfo()
	campaign := config.CampaignFor(workDir.Dir)
	return pathsReport{
		SchemaVersion: pathsSchemaVersion,
		AppSlug:       config.AppSlug,
		InstallRoot:   root,
		ConfigFile:    config.ConfigFile(),
		ServeDir:      filepath.Join(root, "serve"),
		CacheDir:      config.DefaultCacheDir(),
		ModelsDir:     config.ModelsDir(),
		SessionsDir:   config.SessionsDir(),
		CapturesDir:   config.CapturesDir(),
		MeetingsDir:   config.MeetingsDir(),
		PersonasDir:   config.PersonasDir(),
		PromptsDir:    config.PromptsDir(),
		SkillsDir:     config.SkillsDir(),
		WorkDir:       workDir.Dir,

		WorkDirSource:       string(workDir.Source),
		WorkDirSourceDetail: workDir.Describe(),
		Campaign: campaignReport{
			Found: campaign.Found(),
			Name:  campaign.Name,
			ID:    campaign.ID,
			Root:  campaign.Root,
		},
	}
}

func runPaths(cmd *cobra.Command, report pathsReport, asJSON bool) error {
	out := cmd.OutOrStdout()

	if asJSON {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(report)
	}

	rows := []struct{ label, path string }{
		{"Install root", report.InstallRoot},
		{"Config file", report.ConfigFile},
		{"Serve credentials", report.ServeDir},
		{"Cache", report.CacheDir},
		{"Models", report.ModelsDir},
		{"Sessions", report.SessionsDir},
		{"Captures", report.CapturesDir},
		{"Meetings", report.MeetingsDir},
		{"Personas", report.PersonasDir},
		{"Prompts", report.PromptsDir},
		{"Skills", report.SkillsDir},
		{"Work dir", report.WorkDir},
		{"Work dir source", report.WorkDirSource + " (" + report.WorkDirSourceDetail + ")"},
		{"Campaign", humanCampaign(report.Campaign)},
	}
	fmt.Fprintf(out, "\n  %s\n", titleStyle.Render("Obey Voice Paths"))
	fmt.Fprintf(out, "  %s\n\n", dimStyle.Render("app slug: "+report.AppSlug))
	for _, r := range rows {
		fmt.Fprintf(out, "  %s  %s\n", keyStyle.Render(fmt.Sprintf("%-17s", r.label)), valueStyle.Render(r.path))
	}
	fmt.Fprintln(out)
	return nil
}

// humanCampaign renders the campaign row. The rows are one label and one value,
// so the "none" case says why in the value rather than leaving a blank line the
// reader has to interpret.
func humanCampaign(c campaignReport) string {
	if !c.Found {
		return "none (work dir is not inside a campaign)"
	}
	return config.Campaign{Name: c.Name, ID: c.ID, Root: c.Root}.Describe()
}

func init() {
	pathsCmd.Flags().BoolVar(&pathsJSON, "json", false, "Output machine-readable JSON")
	rootCmd.AddCommand(pathsCmd)
}
