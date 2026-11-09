package decider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/decision"
)

// Server owns a native worker and a pre-bound, typed loopback adapter. The
// general-purpose native inference API is reachable only through its private
// Unix socket, never through the application's loopback endpoint.
type Server struct {
	device, model, endpoint, logPath string
	cancel                           context.CancelFunc
	done                             chan struct{}
	once                             sync.Once
}

func (s *Server) RuntimeInfo() string   { return s.model + " · " + s.device + " · native" }
func (s *Server) Endpoint() string      { return s.endpoint + "/v1/systemone" }
func (s *Server) LogPath() string       { return s.logPath }
func (s *Server) Done() <-chan struct{} { return s.done }
func (s *Server) Close() error          { s.once.Do(s.cancel); <-s.done; return nil }
func Start(ctx context.Context, modelsDir, tier string) (*Server, error) {
	return StartFor(ctx, modelsDir, tier, defaultDevice())
}
func StartFor(ctx context.Context, modelsDir, tier, device string) (*Server, error) {
	device, err := ResolveDevice(ctx, device)
	if err != nil {
		return nil, err
	}
	model, err := ModelFor(tier)
	if err != nil {
		return nil, err
	}
	modelsDir, err = filepath.Abs(modelsDir)
	if err != nil {
		return nil, err
	}
	if err = verifyNative(ctx, modelsDir, tier, device); err != nil {
		return nil, fmt.Errorf("native decisions require setup (models ensure --decision): %w", err)
	}
	modelDir, err := ModelDir(modelsDir, tier)
	if err != nil {
		return nil, err
	}
	if len(model.Files) != 1 {
		return nil, fmt.Errorf("invalid native model manifest")
	}
	weights := filepath.Join(modelDir, model.Files[0].Name)
	hash, err := fileHash(ctx, weights)
	if err != nil {
		return nil, err
	}
	if hash != model.Files[0].SHA256 {
		return nil, fmt.Errorf("native decision model checksum mismatch")
	}
	return startNative(ctx, modelsDir, weights, model, device)
}

func startNative(ctx context.Context, modelsDir, weights string, model Model, device string) (*Server, error) {
	listener, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		return nil, err
	}
	// Keep socket paths short enough for macOS sockaddr_un. MkdirTemp creates
	// mode 0700; no other owner can bind or access this worker's socket.
	scratch, err := os.MkdirTemp("/tmp", "obey-decision-")
	if err != nil {
		listener.Close()
		return nil, err
	}
	socket := filepath.Join(scratch, "worker.sock")
	log, err := os.CreateTemp(RuntimeDir(modelsDir, device), "server-*.log")
	if err != nil {
		listener.Close()
		os.RemoveAll(scratch)
		return nil, err
	}
	processCtx, cancel := context.WithCancel(ctx)
	transport := &http.Transport{Proxy: nil, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}
	scorer := &nativeScorer{client: &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, base: "http://native", model: model}
	api := &http.Server{Handler: nativeHandler(scorer, model, device), ReadHeaderTimeout: 2 * time.Second, ReadTimeout: 10 * time.Second, IdleTimeout: time.Second}
	s := &Server{model: model.Name, device: device, endpoint: "http://" + listener.Addr().String(), logPath: log.Name(), done: make(chan struct{})}
	s.cancel = func() { cancel(); _ = api.Close() }
	args := []string{"-m", weights, "--host", socket, "-c", "40960", "--parallel", "1", "--no-webui", "--cache-ram", "0", "--no-context-shift", "--no-warmup"}
	if device == "cpu" {
		args = append(args, "-ngl", "0")
	} else {
		args = append(args, "-ngl", "999")
		if device == "cuda" {
			args = append(args, "--device", "CUDA0")
		} else {
			args = append(args, "--device", "MTL0")
		}
	}
	cmd := exec.CommandContext(processCtx, NativePath(modelsDir, device), args...)
	cmd.Env = append(cleanEnvironment(), "LD_LIBRARY_PATH="+RuntimeDir(modelsDir, device))
	cmd.Stdout, cmd.Stderr = log, log
	fd, err := listener.File()
	if err != nil {
		s.cancel()
		listener.Close()
		log.Close()
		os.RemoveAll(scratch)
		return nil, err
	}
	owner, err := startOwnedProcess(cmd, fd)
	fd.Close()
	if err != nil {
		s.cancel()
		listener.Close()
		log.Close()
		os.RemoveAll(scratch)
		return nil, err
	}
	go func() {
		_ = cmd.Wait()
		_ = owner.Close()
		s.cancel()
		listener.Close()
		transport.CloseIdleConnections()
		log.Close()
		os.RemoveAll(scratch)
		close(s.done)
	}()
	readyCtx, readyCancel := context.WithTimeout(processCtx, 2*time.Minute)
	defer readyCancel()
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		if nativeHealthy(readyCtx, scorer.client, scorer.base) {
			break
		}
		select {
		case <-s.done:
			return nil, nativeStartupError(s.logPath, fmt.Errorf("worker exited before ready"))
		case <-readyCtx.Done():
			_ = s.Close()
			if ctx.Err() != nil {
				return nil, fmt.Errorf("native decision startup: %w", ctx.Err())
			}
			return nil, nativeStartupError(s.logPath, readyCtx.Err())
		case <-tick.C:
		}
	}
	_, err = scorer.evaluate(readyCtx, json.RawMessage(`{"value":1}`), map[string]decision.Question{"positive": {Type: "noul", Instructions: "Is value positive?"}})
	if err != nil {
		_ = s.Close()
		return nil, fmt.Errorf("native decision warmup: %w", err)
	}
	// File/ExtraFiles temporarily put the shared descriptor into blocking
	// mode. The Go adapter owns Accept, so restore poller mode after both
	// supervisor and worker exec have completed; otherwise Close can hang.
	raw, err := listener.SyscallConn()
	if err == nil {
		var nonblockErr error
		err = raw.Control(func(fd uintptr) { nonblockErr = syscall.SetNonblock(int(fd), true) })
		if err == nil {
			err = nonblockErr
		}
	}
	if err != nil {
		_ = s.Close()
		return nil, fmt.Errorf("restore decision listener: %w", err)
	}
	go func() {
		if err := api.Serve(listener); err != nil && err != http.ErrServerClosed {
			s.cancel()
		}
	}()
	return s, nil
}

func nativeStartupError(logPath string, cause error) error {
	// Surface loader failures rather than asking users to infer missing host
	// dependencies from a readiness timeout. Read only a bounded log tail.
	f, err := os.Open(logPath)
	if err == nil {
		defer f.Close()
		if info, err := f.Stat(); err == nil && info.Size() > 4096 {
			_, _ = f.Seek(-4096, io.SeekEnd)
		}
		raw, _ := io.ReadAll(io.LimitReader(f, 4096))
		log := string(raw)
		if strings.Contains(log, "GLIBC_") || strings.Contains(log, "GLIBCXX_") || strings.Contains(log, "CXXABI_") || strings.Contains(log, "error while loading shared libraries") {
			return fmt.Errorf("native decision host libraries unavailable: use Ubuntu 24.04+/Debian 13+ with libstdc++, OpenSSL 3 and libgomp installed, or configure an external local server; details: %s: %w", logPath, cause)
		}
	}
	return fmt.Errorf("native decision startup failed (%s): %w", logPath, cause)
}

func nativeHealthy(ctx context.Context, client *http.Client, base string) bool {
	ctx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/health", nil)
	if err != nil {
		return false
	}
	r, err := client.Do(req)
	if err != nil {
		return false
	}
	defer r.Body.Close()
	var out struct {
		Status string `json:"status"`
	}
	return r.StatusCode == 200 && json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&out) == nil && out.Status == "ok"
}
func nativeHandler(scorer *nativeScorer, model Model, device string) http.Handler {
	busy := make(chan struct{}, 1)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Origin") != "" {
			http.Error(w, "browser access disabled", http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/health" && r.Method == http.MethodGet {
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "model": model.Name, "device": device, "runtime": "native"})
			return
		}
		if r.URL.Path != "/v1/systemone" || r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		select {
		case busy <- struct{}{}:
			defer func() { <-busy }()
		default:
			http.Error(w, "decision worker busy", http.StatusServiceUnavailable)
			return
		}
		var input struct {
			State     json.RawMessage              `json:"state"`
			Questions map[string]decision.Question `json:"questions"`
			Model     string                       `json:"model"`
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input); err != nil {
			http.Error(w, "invalid decision request", 400)
			return
		}
		var trailing any
		if decoder.Decode(&trailing) != io.EOF {
			http.Error(w, "invalid trailing decision data", 400)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		result, err := scorer.evaluate(ctx, input.State, input.Questions)
		if err != nil {
			http.Error(w, "native decision failed", http.StatusServiceUnavailable)
			return
		}
		_ = json.NewEncoder(w).Encode(result)
	})
}
