package decider

import (
	"fmt"
	"os"
	"os/exec"
)

const supervisorArgument = "--obey-internal-decision-supervisor"

// startOwnedProcess gives the managed worker an independent owner-death watcher.
// The returned write end must stay open until Wait returns. Only this process
// holds it: a crash closes it even when no Go cleanup code can run. Re-executing
// this binary keeps supervision native and independent of the inference engine.
func startOwnedProcess(cmd *exec.Cmd, listener *os.File) (*os.File, error) {
	if _, err := exec.LookPath(cmd.Path); err != nil {
		return nil, fmt.Errorf("find decision worker: %w", err)
	}
	executable, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("find native decision supervisor: %w", err)
	}
	ownerRead, ownerWrite, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("create decision owner pipe: %w", err)
	}
	defer ownerRead.Close()
	cmd.Args = append([]string{executable, supervisorArgument, cmd.Path}, cmd.Args...)
	cmd.Path = executable
	// These descriptors are the supervisor protocol: listener 3, owner pipe 4.
	cmd.ExtraFiles = []*os.File{listener, ownerRead}
	configureProcess(cmd)
	if err := cmd.Start(); err != nil {
		ownerWrite.Close()
		return nil, fmt.Errorf("start decision supervisor: %w", err)
	}
	return ownerWrite, nil
}
