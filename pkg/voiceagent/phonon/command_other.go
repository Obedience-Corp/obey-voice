//go:build !unix

package phonon

import (
	"os/exec"
	"time"
)

func configureCommand(cmd *exec.Cmd)  { cmd.WaitDelay = 2 * time.Second }
func killCommand(cmd *exec.Cmd) error { return cmd.Process.Kill() }
