package decider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
)

// Server owns exactly the process and loopback listener it starts. It never
// probes or terminates another installation's server or claims a fixed port.
type Server struct {
	endpoint string
	logPath  string
	cancel   context.CancelFunc
	done     chan struct{}
	once     sync.Once
}

func (s *Server) Endpoint() string      { return s.endpoint + "/v1/systemone" }
func (s *Server) LogPath() string       { return s.logPath }
func (s *Server) Done() <-chan struct{} { return s.done }
func (s *Server) Close() error          { s.once.Do(s.cancel); <-s.done; return nil }

// Start waits for health and a synthetic warmup only on the setup goroutine.
// The pre-bound listener is inherited by the child, eliminating a free-port
// race and keeping transcript traffic away from unrelated local servers.
func Start(ctx context.Context, modelsDir, tier string) (*Server, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	absolute, err := filepath.Abs(modelsDir)
	if err != nil {
		return nil, fmt.Errorf("resolve decision models directory: %w", err)
	}
	modelsDir = absolute
	if !PythonReady(modelsDir) {
		return nil, fmt.Errorf("managed decision runtime is not installed")
	}
	modelDir, err := ModelDir(modelsDir, tier)
	if err != nil {
		return nil, err
	}
	listener, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		return nil, fmt.Errorf("listen for decisions: %w", err)
	}
	defer listener.Close()
	fd, err := listener.File()
	if err != nil {
		return nil, fmt.Errorf("inherit decision listener: %w", err)
	}
	defer fd.Close()
	log, err := os.CreateTemp(filepath.Join(modelsDir, "decider", runtimeID()), "server-*.log")
	if err != nil {
		return nil, fmt.Errorf("open decision runtime log: %w", err)
	}
	processCtx, cancel := context.WithCancel(ctx)
	s := &Server{endpoint: "http://" + listener.Addr().String(), logPath: log.Name(), cancel: cancel, done: make(chan struct{})}
	cmd := exec.CommandContext(processCtx, PythonPath(modelsDir), "-m", "uvicorn", "decider.serve:app", "--fd", "3", "--no-access-log")
	cmd.ExtraFiles = []*os.File{fd}
	cmd.Dir = filepath.Join(modelsDir, "decider")
	cmd.Env = append(cleanEnvironment(), "DECIDER_MODEL="+modelDir, "DECIDER_DEVICE=auto", "DECIDER_COMPILE=0", "DECIDER_FP8=0", "HF_HUB_OFFLINE=1", "TRANSFORMERS_OFFLINE=1", "HF_HOME="+filepath.Join(modelsDir, "decider", "hf"))
	cmd.Stdout, cmd.Stderr = log, log
	configureProcess(cmd)
	if err := cmd.Start(); err != nil {
		cancel()
		log.Close()
		return nil, fmt.Errorf("start decision runtime: %w", err)
	}
	go func() { _ = cmd.Wait(); _ = log.Close(); close(s.done) }()
	// Close both parent descriptors now; only the owned child serves this port.
	_ = listener.Close()
	_ = fd.Close()
	readyCtx, readyCancel := context.WithTimeout(processCtx, 2*time.Minute)
	defer readyCancel()
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	client := &http.Client{Transport: transport, Timeout: 500 * time.Millisecond, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	defer transport.CloseIdleConnections()
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		if healthy(readyCtx, client, s.endpoint, modelDir) {
			break
		}
		select {
		case <-s.done:
			_ = s.Close()
			return nil, fmt.Errorf("decision runtime stopped before ready (details: %s)", s.logPath)
		case <-readyCtx.Done():
			_ = s.Close()
			return nil, fmt.Errorf("decision runtime readiness: %w", readyCtx.Err())
		case <-tick.C:
		}
	}
	client.Timeout = 30 * time.Second
	warmup := []byte(`{"model":"decider","state":{"value":1},"questions":{"positive":{"type":"noul","instructions":"Is value positive?"}}}`)
	req, err := http.NewRequestWithContext(readyCtx, http.MethodPost, s.Endpoint(), bytes.NewReader(warmup))
	if err == nil {
		req.Header.Set("Content-Type", "application/json")
		var resp *http.Response
		resp, err = client.Do(req)
		if err == nil {
			_, err = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
			_ = resp.Body.Close()
			if resp.StatusCode != 200 {
				err = fmt.Errorf("warmup HTTP %d", resp.StatusCode)
			}
		}
	}
	if err != nil {
		_ = s.Close()
		return nil, fmt.Errorf("decision warmup: %w", err)
	}
	return s, nil
}

func healthy(ctx context.Context, client *http.Client, endpoint, model string) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"/health", nil)
	if err != nil {
		return false
	}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return false
	}
	var health struct {
		OK    bool   `json:"ok"`
		Model string `json:"model"`
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&health) == nil && health.OK && health.Model == model
}
