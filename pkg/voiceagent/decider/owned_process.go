package decider

import (
	_ "embed"
	"fmt"
	"os"
	"os/exec"
)

//go:embed worker_supervisor.py
var workerSupervisor string

// startOwnedProcess gives the managed worker an independent owner-death watcher.
// The returned write end must stay open until Wait returns. Only this process
// holds it: a crash closes it even when no Go cleanup code can run. The Python
// supervisor uses the already installed interpreter and stdlib, so this does
// not change the dependency runtime ID or reinstall any model environment.
func startOwnedProcess(cmd *exec.Cmd, listener *os.File) (*os.File, error) {
	ownerRead, ownerWrite, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("create decision owner pipe: %w", err)
	}
	defer ownerRead.Close()
	cmd.Args = append([]string{cmd.Args[0], "-c", workerSupervisor}, cmd.Args[1:]...)
	// These descriptors are the supervisor protocol: listener 3, owner pipe 4.
	cmd.ExtraFiles = []*os.File{listener, ownerRead}
	configureProcess(cmd)
	if err := cmd.Start(); err != nil {
		ownerWrite.Close()
		return nil, fmt.Errorf("start decision supervisor: %w", err)
	}
	return ownerWrite, nil
}
