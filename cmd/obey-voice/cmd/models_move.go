package cmd

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

var (
	modelsMoveJSON  bool
	modelsUsageJSON bool
)

// modelsUsage is `models usage --json`.
type modelsUsage struct {
	ModelsDir string `json:"models_dir"`
	Source    string `json:"source"`
	Bytes     int64  `json:"bytes"`
	Files     int    `json:"files"`
}

var modelsUsageCmd = &cobra.Command{
	Use:   "usage",
	Short: "Show where models are stored and how much space they use",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		cfg, err := config.LoadRaw()
		if err != nil {
			return err
		}
		dir := config.ModelsDirFrom(cfg)
		bytes, files, err := config.ModelsUsage(cmd.Context(), dir)
		if err != nil {
			return fmt.Errorf("measuring %s: %w", dir, err)
		}
		usage := modelsUsage{ModelsDir: dir, Source: config.Source("models_dir"), Bytes: bytes, Files: files}
		out := cmd.OutOrStdout()
		if modelsUsageJSON {
			return json.NewEncoder(out).Encode(usage)
		}
		fmt.Fprintf(out, "  Models folder: %s\n  Size: %s in %d files\n", dir, config.FormatBytes(bytes), files)
		return nil
	},
}

var modelsMoveCmd = &cobra.Command{
	Use:   "move <folder>",
	Short: "Move every model to another folder and use it from now on",
	Long: `Move every model to another folder and use it from now on.

The files are moved, models_dir is saved, and any other setting that named a
file inside the old folder is updated to the same file in the new one. The old
folder is removed only after the new one is complete and saved; a failure or
Ctrl-C before that leaves the old folder in use and removes the partial copy.

The destination must be empty or not exist yet. To start using a folder that
already holds models, run 'obey-voice config set models_dir <folder>' instead.

Stop any running conversation or meeting first: a running one keeps reading
from the old folder.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.LoadRaw()
		if err != nil {
			return err
		}
		out := cmd.OutOrStdout()
		enc := json.NewEncoder(out)
		lastPhase, lastTenth := "", int64(-1)
		result, err := config.MoveModels(cmd.Context(), cfg, args[0], config.MoveOptions{Progress: func(p config.MoveProgress) {
			tenth := int64(0)
			if p.Total > 0 {
				tenth = p.Done * 10 / p.Total
			}
			if p.Phase == lastPhase && tenth == lastTenth {
				return
			}
			lastPhase, lastTenth = p.Phase, tenth
			if modelsMoveJSON {
				_ = enc.Encode(p)
				return
			}
			if p.Phase == config.MovePhaseCopy {
				fmt.Fprintf(out, "  %s: %s of %s\n", p.Phase, config.FormatBytes(p.Done), config.FormatBytes(p.Total))
				return
			}
			fmt.Fprintf(out, "  %s…\n", p.Phase)
		}})
		if err != nil {
			return fmt.Errorf("models move: %w", err)
		}
		if modelsMoveJSON {
			return enc.Encode(result)
		}
		fmt.Fprintf(out, "  Moved %s (%d files) from %s to %s.\n", config.FormatBytes(result.Bytes), result.Files, result.From, result.To)
		for _, key := range result.RewrittenKeys {
			fmt.Fprintf(out, "  Updated %s to the new folder.\n", key)
		}
		for _, warning := range result.Warnings {
			fmt.Fprintf(out, "  Warning: %s\n", warning)
		}
		return nil
	},
}

func init() {
	modelsUsageCmd.Flags().BoolVar(&modelsUsageJSON, "json", false, "Output machine-readable JSON")
	modelsMoveCmd.Flags().BoolVar(&modelsMoveJSON, "json", false, "Stream NDJSON progress on stdout, then one result line")
	modelsCmd.AddCommand(modelsUsageCmd, modelsMoveCmd)
}
