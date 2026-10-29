//go:build linux || darwin

package brain

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestConfigureCommandCancellationKillsProcessGroup(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cmd := exec.CommandContext(ctx, "sh", "-c", `sleep 30 & child=$!; printf '%s\n' "$child"; wait`)
	configureCommandCancellation(cmd)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start command: %v", err)
	}
	t.Cleanup(func() {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	})

	childLine, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil {
		t.Fatalf("read child PID: %v", err)
	}
	childPID, err := strconv.Atoi(strings.TrimSpace(childLine))
	if err != nil {
		t.Fatalf("parse child PID %q: %v", childLine, err)
	}
	t.Cleanup(func() {
		if childPID > 0 {
			_ = syscall.Kill(childPID, syscall.SIGKILL)
		}
	})

	cancel()
	if err := cmd.Wait(); err == nil {
		t.Fatal("cancelled command exited successfully, want signal error")
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		err := syscall.Kill(childPID, 0)
		if errors.Is(err, syscall.ESRCH) || (err == nil && linuxZombie(childPID)) {
			childPID = 0
			return
		}
		if err != nil {
			t.Fatalf("probe child process %d: %v", childPID, err)
		}
		if time.Now().After(deadline) {
			t.Fatalf("child process %d survived command cancellation", childPID)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A killed child may remain as a zombie until PID 1 reaps it, particularly in
// containers. kill(pid, 0) still succeeds for that dead process. The cancellation
// contract is that the descendant stops executing, not that init has reaped it.
func linuxZombie(pid int) bool {
	if runtime.GOOS != "linux" {
		return false
	}
	status, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	if err != nil {
		return false
	}
	for line := range strings.SplitSeq(string(status), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[0] == "State:" {
			return fields[1] == "Z"
		}
	}
	return false
}
