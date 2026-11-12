package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync/atomic"

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

// The --json stream: one object per line, each with a type.
type moveProgressLine struct {
	Type string `json:"type"`
	config.MoveProgress
}

type moveResultLine struct {
	Type string `json:"type"`
	config.MoveResult
}

type movePlanLine struct {
	Type string `json:"type"`
	config.MovePlan
}

type moveErrorLine struct {
	Type     string   `json:"type"`
	Kind     string   `json:"kind"`
	Message  string   `json:"message"`
	Refusals []string `json:"refusals,omitempty"`
}

var modelsMoveCmd = &cobra.Command{
	Use:   "move <folder>",
	Short: "Move every model to another folder and use it from now on",
	Long: `Move every model to another folder and use it from now on.

On the same drive the folder is renamed, and until the new folder is saved a
link at the old path leads to it (where the drive can hold links). On another drive the files are copied and
flushed to disk, then models_dir is saved together with any other setting that
named a file inside the old folder. Only then is the old folder set aside (as
<folder>.obey-voice-old-<time>) and every file in it read and compared with
what was copied; anything that differs is copied again. The set-aside folder
is deleted only if a second look finds nothing still changing and it holds
nothing obey-voice does not use; otherwise it is kept, with a note inside, and
the move says why.

Ctrl-C, or SIGTERM from another program, stops the move before the save: the
old folder stays in use and only what this move created is removed (an empty
destination you made yourself is kept). After the save the move finishes. A
second Ctrl-C quits at once and leaves the cleanup unfinished. A cancelled move
exits with status 130.

The destination must be a full path to a folder that is empty or does not
exist yet. A link is followed and the files land in its target. To start using
a folder that already holds models, run 'obey-voice config set models_dir
<folder>' instead.

The move is refused, and nothing is changed, when: models_dir is set by
MODELS_DIR or is a relative path; models_dir is your home folder, a standard
folder such as Documents, the top of a drive, or holds the settings folder;
either folder is inside the other; another setting set in the environment names
a file in the old folder; the folder holds something other than files, folders
and links, links that would point elsewhere after the move, or another drive
mounted inside it; the other drive lacks space; or a decision model install,
'obey-voice serve' or another move is running.

The move cannot see other programs reading or writing the folder: a
conversation or meeting in another window, the desktop app's engine, or a
download in progress. Stop them first.

--dry-run prints what would move, the sizes, the entries obey-voice does not
use (which keep the old folder), the destination and any reason the move would
be refused, and changes nothing. With --json every line is an object with a
"type": "progress", "result", "plan" or "error".`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		// A desktop app that quits mid-move closes this pipe; the move must
		// still finish rather than die on its next progress line.
		dropBrokenPipeSignals()
		cfg, err := config.LoadRaw()
		if err != nil {
			return err
		}
		from := config.ModelsDirFrom(cfg)
		var phase atomic.Value
		phase.Store(config.MovePhaseMeasure)
		ctx, stop := interruptContext(cmd.Context(), func(sig os.Signal, first bool) {
			announceMoveSignal(cmd.ErrOrStderr(), phase.Load().(string), from, sig, first)
		})
		defer stop()
		personas, _ := cleanPersonaSources(cfg)
		opts := config.MoveOptions{Personas: personas}
		if modelsMoveDryRun {
			return runModelsMoveDryRun(ctx, cmd, cfg, args[0], opts)
		}
		out := cmd.OutOrStdout()
		enc := json.NewEncoder(out)
		lastPhase, lastTenth := "", int64(-1)
		opts.Progress = func(p config.MoveProgress) {
			phase.Store(p.Phase)
			tenth := int64(0)
			if p.Total > 0 {
				tenth = p.Done * 10 / p.Total
			}
			if p.Phase == lastPhase && tenth == lastTenth {
				return
			}
			lastPhase, lastTenth = p.Phase, tenth
			if modelsMoveJSON {
				_ = enc.Encode(moveProgressLine{Type: "progress", MoveProgress: p})
				return
			}
			if p.Total > 0 {
				fmt.Fprintf(out, "  %s: %s of %s\n", p.Phase, config.FormatBytes(p.Done), config.FormatBytes(p.Total))
				return
			}
			fmt.Fprintf(out, "  %s…\n", p.Phase)
		}
		result, err := config.MoveModels(ctx, cfg, args[0], opts)
		if err != nil {
			return modelsMoveError(cmd, err, from)
		}
		if modelsMoveJSON {
			return enc.Encode(moveResultLine{Type: "result", MoveResult: result})
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

// announceMoveSignal says what a signal does to the move, so a person at the
// terminal, or the desktop app's task log, knows to wait for the cleanup.
func announceMoveSignal(w io.Writer, phase, from string, sig os.Signal, first bool) {
	committed := config.MovePhaseCommitted(phase)
	switch {
	case first && committed:
		fmt.Fprintln(w, "  The new folder is already saved, so the move is finishing. Ctrl-C again quits now and leaves the old folder behind.")
	case first:
		fmt.Fprintln(w, "  Stopping the move and removing what it copied. Ctrl-C again quits now and leaves that copy behind.")
	case sig != os.Interrupt:
		fmt.Fprintln(w, "  Still finishing the move.")
	case committed:
		fmt.Fprintf(w, "  Quit before the move finished: models_dir names the new folder, and the old one may be left beside %s as <name>.obey-voice-old-<time>.\n", from)
	default:
		fmt.Fprintf(w, "  Quit before the copy was removed: the destination holds a partial copy marked %s; delete what is inside it.\n", ".obey-voice-move-incomplete")
	}
}

func modelsMoveError(cmd *cobra.Command, err error, from string) error {
	var refused *config.MoveRefusedError
	line := moveErrorLine{Type: "error", Kind: "failed", Message: err.Error()}
	var out error
	switch {
	case errors.Is(err, context.Canceled):
		line.Kind = "cancelled"
		line.Message = "cancelled; nothing was moved and the models are still in " + from
		out = &ExitCodeError{Code: 130, Err: fmt.Errorf("models move: %s", line.Message)}
	case errors.As(err, &refused):
		line.Kind, line.Refusals = "refused", refused.Reasons
		if len(refused.Reasons) > 1 {
			out = fmt.Errorf("models move: refused:\n  - %s", strings.Join(refused.Reasons, "\n  - "))
		}
	}
	if out == nil {
		out = fmt.Errorf("models move: %w", err)
	}
	if modelsMoveJSON {
		_ = json.NewEncoder(cmd.OutOrStdout()).Encode(line)
	}
	return out
}

func runModelsMoveDryRun(ctx context.Context, cmd *cobra.Command, cfg *config.Config, dest string, opts config.MoveOptions) error {
	plan, err := config.PlanModelsMove(ctx, cfg, dest, opts)
	if err != nil {
		return fmt.Errorf("models move: %w", err)
	}
	if modelsMoveJSON {
		if err := json.NewEncoder(cmd.OutOrStdout()).Encode(movePlanLine{Type: "plan", MovePlan: plan}); err != nil {
			return err
		}
	} else {
		printMovePlan(cmd.OutOrStdout(), plan)
	}
	if len(plan.Refusals) > 0 {
		if len(plan.Refusals) == 1 {
			return fmt.Errorf("models move: %s", plan.Refusals[0])
		}
		return fmt.Errorf("models move: refused:\n  - %s", strings.Join(plan.Refusals, "\n  - "))
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
	if plan.Measured {
		fmt.Fprintf(out, "  Size: %s in %d files\n", config.FormatBytes(plan.Bytes), plan.Files)
		if plan.SameVolume {
			fmt.Fprintln(out, "  How:  renamed in place (same drive)")
		} else {
			how := "  How:  copied to the other drive and checked, then the old folder is removed"
			if plan.FreeBytes > 0 {
				how += fmt.Sprintf(" (%s free there)", config.FormatBytes(plan.FreeBytes))
			}
			fmt.Fprintln(out, how)
		}
		if len(plan.Entries) > 0 {
			fmt.Fprintln(out, "  Contents:")
			printMoveEntries(out, plan.Entries)
		}
		if len(plan.Unrecognized) > 0 {
			fmt.Fprintln(out, "  Not used by your settings or personas (moved too; the old folder is then kept, not deleted):")
			printMoveEntries(out, plan.Unrecognized)
		}
		for _, key := range plan.RewriteKeys {
			fmt.Fprintf(out, "  Would update %s to the new folder.\n", key)
		}
	}
	for _, reason := range plan.Refusals {
		fmt.Fprintf(out, "  Refused: %s\n", reason)
	}
}

func printMoveEntries(out io.Writer, entries []config.MoveEntry) {
	width := 0
	for _, e := range entries {
		width = max(width, len(e.Name))
	}
	for _, e := range entries {
		fmt.Fprintf(out, "    %-*s  %s\n", width, e.Name, config.FormatBytes(e.Bytes))
	}
}

func init() {
	modelsUsageCmd.Flags().BoolVar(&modelsUsageJSON, "json", false, "Output machine-readable JSON")
	modelsMoveCmd.Flags().BoolVar(&modelsMoveJSON, "json", false, `One JSON object per line, each with a "type": progress, result, plan or error`)
	modelsMoveCmd.Flags().BoolVar(&modelsMoveDryRun, "dry-run", false, "Show what would move and why it would be refused, and change nothing")
	modelsCmd.AddCommand(modelsUsageCmd, modelsMoveCmd)
}
