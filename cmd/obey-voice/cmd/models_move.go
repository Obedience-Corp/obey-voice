package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

var (
	modelsMoveJSON   bool
	modelsMoveDryRun bool
	modelsUsageJSON  bool
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

On the same drive the folder is renamed. On another drive the files are copied
and flushed to disk, then models_dir is saved together with any other setting
that named a file inside the old folder. Only then is the old folder set aside,
checked against the new one, and deleted. Anything written to it during the
copy is brought over first; if it still differs, it is kept and the move says
where.

Ctrl-C before the save stops the move: the old folder stays in use and the
folders this move created are removed (an empty destination you made yourself
is kept). After the save the move finishes; a second Ctrl-C quits at once.

The destination must be a full path to a folder that is empty or does not exist
yet. A link is followed and the files land in its target. To start using a
folder that already holds models, run 'obey-voice config set models_dir
<folder>' instead.

The move is refused, and nothing is changed, when: models_dir is set by
MODELS_DIR or is a relative path; models_dir is your home folder, a standard
folder such as Documents, the top of a drive, or holds the settings folder;
either folder is inside the other; another setting set in the environment names
a file in the old folder; the folder holds something other than files, folders
and links, or links that would point elsewhere after the move; the other drive
lacks space; or a decision model install, 'obey-voice serve' or another move
is running.

Stop any conversation or meeting in another window first: they keep reading
from the old folder and the move cannot see them.

--dry-run prints what would move, the sizes, the destination and any reason
the move would be refused, and changes nothing.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, stop := interruptContext(cmd.Context())
		defer stop()
		cfg, err := config.LoadRaw()
		if err != nil {
			return err
		}
		if modelsMoveDryRun {
			return runModelsMoveDryRun(ctx, cmd, cfg, args[0])
		}
		out := cmd.OutOrStdout()
		enc := json.NewEncoder(out)
		from := config.ModelsDirFrom(cfg)
		lastPhase, lastTenth := "", int64(-1)
		result, err := config.MoveModels(ctx, cfg, args[0], config.MoveOptions{Progress: func(p config.MoveProgress) {
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
			return modelsMoveError(err, from)
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

func modelsMoveError(err error, from string) error {
	var refused *config.MoveRefusedError
	switch {
	case errors.Is(err, context.Canceled):
		return fmt.Errorf("models move: cancelled; nothing was moved and the models are still in %s", from)
	case errors.As(err, &refused) && len(refused.Reasons) > 1:
		return fmt.Errorf("models move: refused:\n  - %s", strings.Join(refused.Reasons, "\n  - "))
	}
	return fmt.Errorf("models move: %w", err)
}

func runModelsMoveDryRun(ctx context.Context, cmd *cobra.Command, cfg *config.Config, dest string) error {
	plan, err := config.PlanModelsMove(ctx, cfg, dest)
	if err != nil {
		return fmt.Errorf("models move: %w", err)
	}
	if modelsMoveJSON {
		if err := writeJSON(cmd, plan); err != nil {
			return err
		}
	} else {
		printMovePlan(cmd.OutOrStdout(), plan)
	}
	if len(plan.Refusals) > 0 {
		return modelsMoveError(&config.MoveRefusedError{Reasons: plan.Refusals}, plan.From)
	}
	return nil
}

func printMovePlan(out io.Writer, plan config.MovePlan) {
	fmt.Fprintln(out, "  Dry run: nothing was changed.")
	if plan.From != "" {
		fmt.Fprintf(out, "  From: %s\n", plan.From)
	}
	if plan.To != "" {
		fmt.Fprintf(out, "  To:   %s\n", plan.To)
	}
	if plan.Dest != "" && plan.Dest != plan.To {
		fmt.Fprintf(out, "        (a link; the files land in %s)\n", plan.Dest)
	}
	if plan.Source == "" || plan.Dest == "" {
		printMoveRefusals(out, plan.Refusals)
		return
	}
	fmt.Fprintf(out, "  Size: %s in %d files\n", config.FormatBytes(plan.Bytes), plan.Files)
	if plan.SameVolume {
		fmt.Fprintln(out, "  How:  renamed in place (same drive)")
	} else {
		how := "  How:  copied to the other drive, then the old folder is removed"
		if plan.FreeBytes > 0 {
			how += fmt.Sprintf(" (%s free there)", config.FormatBytes(plan.FreeBytes))
		}
		fmt.Fprintln(out, how)
	}
	if len(plan.Entries) > 0 {
		fmt.Fprintln(out, "  Contents:")
		width := 0
		for _, e := range plan.Entries {
			width = max(width, len(e.Name))
		}
		for _, e := range plan.Entries {
			fmt.Fprintf(out, "    %-*s  %s\n", width, e.Name, config.FormatBytes(e.Bytes))
		}
	}
	for _, key := range plan.RewriteKeys {
		fmt.Fprintf(out, "  Would update %s to the new folder.\n", key)
	}
	printMoveRefusals(out, plan.Refusals)
}

func printMoveRefusals(out io.Writer, refusals []string) {
	for _, reason := range refusals {
		fmt.Fprintf(out, "  Refused: %s\n", reason)
	}
}

func init() {
	modelsUsageCmd.Flags().BoolVar(&modelsUsageJSON, "json", false, "Output machine-readable JSON")
	modelsMoveCmd.Flags().BoolVar(&modelsMoveJSON, "json", false, "Stream NDJSON progress on stdout, then one result line (with --dry-run: the plan)")
	modelsMoveCmd.Flags().BoolVar(&modelsMoveDryRun, "dry-run", false, "Show what would move and why it would be refused, and change nothing")
	modelsCmd.AddCommand(modelsUsageCmd, modelsMoveCmd)
}
