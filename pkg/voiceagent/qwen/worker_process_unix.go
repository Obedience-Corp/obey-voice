//go:build darwin || dragonfly || freebsd || illumos || linux || netbsd || openbsd || solaris

package qwen

import (
	"os/exec"
	"syscall"
	"time"
)

// WorkerProcessWaitDelay bounds how long a killed worker may hold its pipes.
const WorkerProcessWaitDelay = 2 * time.Second

// ConfigureWorkerCommand puts the worker in its own process group so cancellation
// also terminates helpers forked by native GPU/runtime stacks.
func ConfigureWorkerCommand(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	cmd.WaitDelay = WorkerProcessWaitDelay
}
