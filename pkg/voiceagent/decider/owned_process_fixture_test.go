//go:build darwin || linux

package decider

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

type workerInfo struct {
	Worker     int `json:"worker"`
	Descendant int `json:"descendant"`
	Supervisor int `json:"supervisor"`
}

type ownerFixture struct {
	cmd      *exec.Cmd
	input    io.WriteCloser
	group    int
	address  string
	endpoint string
	client   *http.Client
	done     chan struct{}
}

func ownedFixtureCommand(ctx context.Context, python string) *exec.Cmd {
	return exec.CommandContext(ctx, python, "-c", workerFixture)
}

func startOwnerFixture(t *testing.T, slowStartup bool) *ownerFixture {
	t.Helper()
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is required for the model-free lifecycle fixture")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestOwnedWorkerOwnerHelper$")
	cmd.Env = append(os.Environ(), "OBEY_DECIDER_OWNER_HELPER=1", "OBEY_DECIDER_TEST_PYTHON="+python,
		fmt.Sprintf("OBEY_DECIDER_TEST_SLOW_START=%t", slowStartup))
	cmd.WaitDelay = time.Second
	input, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		cancel()
		t.Fatal(err)
	}
	fixture := &ownerFixture{cmd: cmd, input: input, done: make(chan struct{}),
		client: &http.Client{Timeout: 200 * time.Millisecond, Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true}}}
	go func() { _ = cmd.Wait(); close(fixture.done) }()
	t.Cleanup(func() {
		if fixture.group > 0 {
			_ = syscall.Kill(-fixture.group, syscall.SIGKILL)
		}
		cancel()
		input.Close()
		fixture.client.CloseIdleConnections()
		fixture.wait(t)
	})
	var ready struct {
		Address string
		Group   int
	}
	decoded := make(chan error, 1)
	go func() { decoded <- json.NewDecoder(stdout).Decode(&ready) }()
	select {
	case err := <-decoded:
		if err != nil {
			t.Fatalf("read owner fixture: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("owner fixture did not report its process group")
	}
	fixture.group, fixture.address = ready.Group, ready.Address
	fixture.endpoint = "http://" + ready.Address
	return fixture
}

// This helper is a real owning process. Killing it bypasses all cancellation
// and deferred cleanup, matching the crash that left inference workers behind.
func TestOwnedWorkerOwnerHelper(t *testing.T) {
	if os.Getenv("OBEY_DECIDER_OWNER_HELPER") != "1" {
		return
	}
	listener, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	file, err := listener.File()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := ownedFixtureCommand(ctx, os.Getenv("OBEY_DECIDER_TEST_PYTHON"))
	cmd.Env = append(cleanEnvironment(), "FIXTURE_SLOW_START="+os.Getenv("OBEY_DECIDER_TEST_SLOW_START"))
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	var lease *os.File
	if os.Getenv("OBEY_DECIDER_TEST_BASELINE") == "1" {
		// Opt-in evidence only: reproduce the old direct launch, then let the
		// regression fail on survival. The outer test always kills its fixture.
		configureProcess(cmd)
		cmd.ExtraFiles = []*os.File{file}
		err = cmd.Start()
	} else {
		lease, err = startOwnedProcess(cmd, file)
	}
	listener.Close()
	file.Close()
	if err != nil {
		t.Fatal(err)
	}
	if lease != nil {
		defer lease.Close()
	}
	if err := json.NewEncoder(os.Stdout).Encode(struct {
		Address string
		Group   int
	}{address, cmd.Process.Pid}); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	control := make(chan struct{})
	go func() { scanner := bufio.NewScanner(os.Stdin); scanner.Scan(); close(control) }()
	select {
	case <-control:
		cancel()
		<-done
	case <-done:
	case <-time.After(30 * time.Second):
		cancel()
		<-done
		t.Fatal("owner fixture timed out")
	}
}

const workerFixture = `
import json, os, socket, subprocess, sys, time
if os.environ.get("FIXTURE_SLOW_START") == "true":
    time.sleep(20)
descendant = subprocess.Popen([sys.executable, "-c", "import time; time.sleep(60)"])
listener = socket.socket(fileno=3)
while True:
    connection, _ = listener.accept()
    with connection:
        request = connection.recv(4096)
        body = json.dumps({"worker": os.getpid(), "descendant": descendant.pid,
                           "supervisor": os.getppid()}).encode()
        connection.sendall(b"HTTP/1.0 200 OK\r\nContent-Length: " + str(len(body)).encode()
                           + b"\r\n\r\n" + body)
    if b" /exit " in request:
        sys.exit(0)
`
