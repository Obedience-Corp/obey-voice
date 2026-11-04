package speechserve

import (
	"bufio"
	"io"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestChildCloseAfterStartupObservesExit(t *testing.T) {
	cmd := exec.CommandContext(t.Context(), "sh", "-c", "exit 3")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exitErr := cmd.Wait()
	if exitErr == nil {
		t.Fatal("fixture child should exit unsuccessfully")
	}
	// Hold stdout open so readReady must observe process exit, rather than
	// racing the stdout EOF path that usually hides this bug on macOS.
	reader, writer := io.Pipe()
	t.Cleanup(func() { _ = reader.Close(); _ = writer.Close() })
	c := &child{cmd: cmd, stdout: bufio.NewReader(reader), done: make(chan struct{}), waitErr: exitErr}
	close(c.done)
	if err := c.readReady(t.Context(), time.Second); err == nil || !strings.Contains(err.Error(), "exited during startup: exit status 3") {
		t.Fatalf("startup lost the child exit status: %v", err)
	}
	closed := make(chan struct{})
	go func() { c.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("cleanup waited for an exit notification already observed during startup")
	}
	// Repeated cleanup and observation must retain the same exit outcome.
	c.Close()
	select {
	case <-c.done:
	default:
		t.Fatal("cleanup consumed the process completion signal")
	}
}
