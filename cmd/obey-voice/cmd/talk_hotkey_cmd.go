//go:build !integration && (windows || (darwin && cgo))

package cmd

import (
	"fmt"
	"io"
	"os"
	"os/signal"
	"runtime"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/Obedience-Corp/obey-voice/internal/talkhotkey"
)

var (
	talkHotkeyCombo string
	talkHotkeyProbe bool
)

// talkHotkeyExitUsage is the exit code for a combo that does not parse. It is
// distinct from 1 (registration failed) so the parent can tell "you asked for
// something impossible" from "something else owns the key".
const talkHotkeyExitUsage = 2

func init() {
	rootCmd.AddCommand(newTalkHotkeyCmd())
}

// newTalkHotkeyCmd builds the helper the TUI spawns to own a system-wide key.
//
// It is a subprocess rather than a goroutine because both platform APIs want a
// thread of their own running an event loop — Carbon's on the process's main
// thread, Win32's on whichever thread registered the key — and it is hidden
// because nobody runs it by hand: its stdout is a machine protocol, and it
// exits when its stdin closes so it cannot outlive the parent still holding
// the key.
func newTalkHotkeyCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:    "talk-hotkey",
		Short:  "Own a system-wide talk key and report presses as JSON lines",
		Hidden: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			combo, err := talkhotkey.ParseCombo(talkHotkeyCombo)
			if err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "obey-voice talk-hotkey: %v\n", err)
				os.Exit(talkHotkeyExitUsage)
			}
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			// A parent that died without stopping us closes this pipe. Without
			// this the helper would keep the combo registered forever, and the
			// user's key would be swallowed by a process nothing points at. It
			// is also how a live parent asks us to leave on Windows, which has
			// no os.Interrupt for a child process.
			go func() {
				_, _ = io.Copy(io.Discard, os.Stdin)
				stop()
			}()
			return talkhotkey.Serve(ctx, combo, cmd.OutOrStdout(), talkHotkeyProbe)
		},
	}
	cmd.Flags().StringVar(&talkHotkeyCombo, "combo", talkhotkey.DefaultComboFor(runtime.GOOS), "Combo to register, e.g. alt+space or ctrl+alt+f13")
	cmd.Flags().BoolVar(&talkHotkeyProbe, "probe", false, "Register, report, and exit — availability check only")
	return cmd
}
