//go:build !integration

package cmd

import (
	"fmt"
	"path/filepath"

	"github.com/Obedience-Corp/obey-voice/internal/netapi"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/spf13/cobra"
)

func serveDevicesCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use: "devices", Short: "Manage permission for paired devices to edit host settings and personas",
		PersistentPreRunE: func(*cobra.Command, []string) error { return nil },
	}
	cmd.AddCommand(&cobra.Command{
		Use: "list", Short: "List paired devices and their settings permission", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			devices, err := netapi.ListConfigDevices(filepath.Join(config.ConfigDir(), "serve"))
			if err != nil {
				return err
			}
			if len(devices) == 0 {
				_, err = fmt.Fprintln(cmd.OutOrStdout(), "No paired devices.")
				return err
			}
			for _, d := range devices {
				permission := "denied"
				if d.ConfigAllowed {
					permission = "allowed"
				}
				if _, err := fmt.Fprintf(cmd.OutOrStdout(), "%s  %q  settings: %s\n", d.ID, d.DeviceName, permission); err != nil {
					return err
				}
			}
			return nil
		},
	})
	for _, allowed := range []bool{true, false} {
		action := "revoke"
		past := "revoked"
		if allowed {
			action = "grant"
			past = "granted"
		}
		cmd.AddCommand(&cobra.Command{
			Use: action + " DEVICE_ID", Short: action + " permission to edit settings and personas", Args: cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				if err := netapi.SetDeviceConfigPermission(filepath.Join(config.ConfigDir(), "serve"), args[0], allowed); err != nil {
					return err
				}
				_, err := fmt.Fprintf(cmd.OutOrStdout(), "Settings permission %s for device %s.\n", past, args[0])
				return err
			},
		})
	}
	return cmd
}
