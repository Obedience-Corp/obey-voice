//go:build !darwin && !linux

package decider

import (
	"context"
	"fmt"
	"os/exec"
)

func configureProcess(cmd *exec.Cmd) {}
func installLock(context.Context, string) (func(), error) {
	return nil, fmt.Errorf("managed local decisions are unavailable on this platform")
}
