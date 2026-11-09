//go:build darwin || linux

package decider

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"

	"golang.org/x/sys/unix"
)

// This private re-exec entry point runs before application startup, including
// CLI configuration and UI initialization. It is not selected by environment
// variables. The caller must supply the owned process group and descriptor
// protocol; an ordinary invocation cannot kill the invoking shell's group.
func init() {
	if len(os.Args) < 2 || os.Args[1] != supervisorArgument {
		return
	}
	if err := superviseDecisionWorker(); err != nil {
		fmt.Fprintln(os.Stderr, "native decision supervisor:", err)
	}
	os.Exit(125)
}

func superviseDecisionWorker() error {
	if len(os.Args) < 4 {
		return fmt.Errorf("missing worker command")
	}
	group, err := syscall.Getpgid(0)
	if err != nil || group != os.Getpid() {
		return fmt.Errorf("requires an owned process group")
	}
	// Validate all descriptors before enabling group cleanup.
	var ownerStat, listenerStat unix.Stat_t
	if err := unix.Fstat(4, &ownerStat); err != nil {
		return fmt.Errorf("owner pipe: %w", err)
	}
	if ownerStat.Mode&unix.S_IFMT != unix.S_IFIFO {
		return fmt.Errorf("owner descriptor is not a pipe")
	}
	if err := unix.Fstat(3, &listenerStat); err != nil {
		return fmt.Errorf("listener: %w", err)
	}
	if listenerStat.Mode&unix.S_IFMT != unix.S_IFSOCK {
		return fmt.Errorf("listener descriptor is not a socket")
	}
	stop := func() { _ = syscall.Kill(-group, syscall.SIGKILL) }
	defer stop()
	owner := os.NewFile(4, "decision-owner")
	defer owner.Close()
	listener := os.NewFile(3, "decision-listener")
	defer listener.Close()
	// Check for a dead owner before loading a potentially large model.
	poll := []unix.PollFd{{Fd: 4, Events: unix.POLLIN | unix.POLLHUP | unix.POLLERR}}
	for {
		n, err := unix.Poll(poll, 0)
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			return fmt.Errorf("poll owner: %w", err)
		}
		if n != 0 {
			return fmt.Errorf("owner already closed")
		}
		break
	}
	// Independent process: a stopped or wedged inference worker cannot prevent
	// this watcher from observing owner death. Only the listener reaches it.
	go func() { var b [1]byte; _, _ = owner.Read(b[:]); stop() }()
	worker := &exec.Cmd{Path: os.Args[2], Args: os.Args[3:], Env: os.Environ(),
		ExtraFiles: []*os.File{listener}, Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr}
	if err := worker.Start(); err != nil {
		return fmt.Errorf("start worker: %w", err)
	}
	_ = listener.Close()
	// On any worker exit, remove descendants as well as the supervisor itself.
	return worker.Wait()
}
