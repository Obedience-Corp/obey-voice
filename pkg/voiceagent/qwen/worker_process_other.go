//go:build !darwin && !dragonfly && !freebsd && !illumos && !linux && !netbsd && !openbsd && !solaris

package qwen

import (
	"os/exec"
	"time"
)

// WorkerProcessWaitDelay bounds how long a killed worker may hold its pipes.
const WorkerProcessWaitDelay = 2 * time.Second

// ConfigureWorkerCommand keeps the provider portable on platforms without the
// Unix process-group APIs. The context still bounds the direct worker process.
func ConfigureWorkerCommand(cmd *exec.Cmd) {
	cmd.WaitDelay = WorkerProcessWaitDelay
}
