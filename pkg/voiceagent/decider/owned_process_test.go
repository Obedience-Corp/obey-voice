//go:build darwin || linux

package decider

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

// These native regressions use only inherited pipes and loopback sockets. They
// create no files, install no runtime, and load no model. Filesystem-backed
// StartFor/install tests run separately in the container recipe.
func TestOwnedWorkerDiesWithOwner(t *testing.T) {
	for _, scenario := range []string{"ready", "inference stopped", "startup"} {
		t.Run(scenario, func(t *testing.T) {
			unrelated := startOwnerFixture(t, false)
			unrelated.info(t)
			owner := startOwnerFixture(t, scenario == "startup")
			if scenario != "startup" {
				worker := owner.info(t)
				if scenario == "inference stopped" {
					// A watchdog thread inside this process could never run now.
					if err := syscall.Kill(worker.Worker, syscall.SIGSTOP); err != nil {
						t.Fatal(err)
					}
				}
			}
			if err := owner.cmd.Process.Kill(); err != nil {
				t.Fatal(err)
			}
			owner.wait(t)
			owner.assertGone(t)
			unrelated.info(t) // Cleanup must never kill another owner's server.
		})
	}
}

func TestOwnedWorkerCancellationAndUnexpectedExit(t *testing.T) {
	for _, scenario := range []string{"context cancellation", "worker exits"} {
		t.Run(scenario, func(t *testing.T) {
			owner := startOwnerFixture(t, false)
			owner.info(t)
			if scenario == "context cancellation" {
				if _, err := fmt.Fprintln(owner.input, "cancel"); err != nil {
					t.Fatal(err)
				}
			} else {
				response, err := owner.client.Get(owner.endpoint + "/exit")
				if err != nil {
					t.Fatal(err)
				}
				response.Body.Close()
			}
			owner.wait(t)
			owner.assertGone(t)
		})
	}
}

func (o *ownerFixture) info(t *testing.T) workerInfo {
	t.Helper()
	var info workerInfo
	until := time.Now().Add(5 * time.Second)
	for time.Now().Before(until) {
		response, err := o.client.Get(o.endpoint)
		if err == nil {
			err = json.NewDecoder(response.Body).Decode(&info)
			response.Body.Close()
			if err == nil && info.Worker > 0 && info.Descendant > 0 {
				return info
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("fixture worker did not become ready")
	return info
}

func (o *ownerFixture) wait(t *testing.T) {
	t.Helper()
	select {
	case <-o.done:
	case <-time.After(5 * time.Second):
		t.Fatal("fixture owner did not exit")
	}
}

func (o *ownerFixture) assertGone(t *testing.T) {
	t.Helper()
	until := time.Now().Add(5 * time.Second)
	for time.Now().Before(until) {
		if errors.Is(syscall.Kill(-o.group, 0), syscall.ESRCH) {
			connection, err := net.DialTimeout("tcp", o.address, 100*time.Millisecond)
			if err != nil {
				return
			}
			connection.Close()
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("owned group %d or listener %s survived its owner", o.group, o.address)
}

func TestOwnedWorkerStartFailureClosesOwnerPipe(t *testing.T) {
	// An invalid executable is enough to exercise pipe cleanup, without fixtures
	// on disk or a Python dependency.
	cmd := ownedFixtureCommand(t.Context(), "/obey-voice-nonexistent-worker")
	listener, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	file, err := listener.File()
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	owner, err := startOwnedProcess(cmd, file)
	if err == nil || owner != nil {
		if owner != nil {
			owner.Close()
		}
		t.Fatalf("owner=%v error=%v, want failed launch without an owner pipe", owner, err)
	}
}

func TestNativeSupervisorRejectsOrdinaryInvocation(t *testing.T) {
	cmd := exec.CommandContext(t.Context(), os.Args[0], supervisorArgument, os.Args[0], os.Args[0])
	// No private group or inherited protocol: never terminate the caller's group.
	if err := cmd.Run(); err == nil {
		t.Fatal("supervisor accepted an ordinary invocation")
	}
}

func TestNativeSupervisorRejectsClosedOwnerBeforeWorkerStart(t *testing.T) {
	listener, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	file, err := listener.File()
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer read.Close()
	write.Close()
	cmd := exec.CommandContext(t.Context(), os.Args[0], supervisorArgument, os.Args[0], os.Args[0], "-test.run=^TestOwnedWorkerDescendantHelper$")
	cmd.Env = append(os.Environ(), "OBEY_DECIDER_DESCENDANT=1")
	cmd.ExtraFiles = []*os.File{file, read}
	configureProcess(cmd)
	if err := cmd.Run(); err == nil {
		t.Fatal("closed owner allowed worker execution")
	}
}
