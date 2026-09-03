package cmd

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Obedience-Corp/obey-voice/internal/talkhotkey"
)

// talkCommands is the whole protocol, in the order a hold sends them.
var talkCommands = []string{"down", "up", "press"}

// newTalkCmd is the shortcut tool's side of the talk key. macOS and Windows
// register a combo themselves; everywhere else — and for anyone who would
// rather bind the key in the tool they already use — this is how a press
// reaches the conversation.
func newTalkCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "talk <down|up|press>",
		Short: "Drive the running conversation's talk key from a shortcut tool",
		Long: `Send one talk-key command to the obey-voice conversation running as you.

  obey-voice talk down     open a spoken turn
  obey-voice talk up       send it, reporting how long the key was held
  obey-voice talk press    both edges at once — a tap

Bind down to your shortcut's press and up to its release for hold-to-talk; a
tool that only reports presses binds press with talk_hotkey_mode: toggle.
Recipes for sway, i3, Hyprland, GNOME and KDE are in docs/usage.md under
"Talk key".`,
		// The root hook loads config and seeds prompt files. This command runs
		// on every keypress and must cost no more than a socket write, so it
		// opts out: it reads no config and touches nothing on disk.
		PersistentPreRunE: func(*cobra.Command, []string) error { return nil },
		RunE: func(cmd *cobra.Command, args []string) error {
			command, err := talkCommandArg(args)
			if err != nil {
				return err
			}
			if err := talkhotkey.SendControl(cmd.Context(), command); err != nil {
				if errors.Is(err, talkhotkey.ErrNoListener) {
					return fmt.Errorf("%w — start one with `obey-voice`", err)
				}
				return err
			}
			return nil
		},
	}
}

func talkCommandArg(args []string) (string, error) {
	if len(args) != 1 {
		return "", usageError("talk takes exactly one of %s; got %d argument(s)",
			strings.Join(talkCommands, ", "), len(args))
	}
	command := strings.ToLower(strings.TrimSpace(args[0]))
	if !slices.Contains(talkCommands, command) {
		return "", usageError("unknown talk command %q; want %s", args[0], strings.Join(talkCommands, ", "))
	}
	return command, nil
}
