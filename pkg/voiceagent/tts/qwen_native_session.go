package tts

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	managedqwen "github.com/Obedience-Corp/obey-voice/pkg/voiceagent/qwen"
)

// nativeWorkerProtocol is frozen in projects/qwen3-tts-native/docs/PROTOCOL.md.
const nativeWorkerProtocol = "qwen3-tts-worker/v1"

// nativeWorkerLabel prefixes this transport's protocol errors.
const nativeWorkerLabel = "native worker"

// nativeQwenSession is a long-lived client for bin/qwen3-tts-worker (no Python).
// Stage A: whole-utterance PCM after synth (one pcm_meta + raw f32le + final).
// Stage B: several numbered pcm_meta chunks during synth, then a final that
// totals them — requested per-request, and only where this host has decided
// chunks can be played without running dry (config.QwenStreamEligible).
// Framing lives in speechConn so the LAN client can reuse it byte for byte.
type nativeQwenSession struct {
	*speechSession

	cmd      *exec.Cmd
	stderr   *limitedBuffer
	wait     chan error
	modelDir string
	protocol string
}

// isNativeWorkerBinary reports whether path names the product native worker.
func isNativeWorkerBinary(path string) bool {
	base := strings.ToLower(filepath.Base(strings.TrimSpace(path)))
	return base == "qwen3-tts-worker" || base == "qwen3-tts-worker.exe"
}

// nativeInstallPaths is the ensure layout under models_dir/qwen3-tts/.
type nativeInstallPaths struct {
	Root     string
	Worker   string
	ModelDir string
	// BackendHint is the package's build backend (metal / cuda / cpu). It is
	// the only thing a local install reports about which silicon it runs on.
	BackendHint string
}

// findNativeInstall looks for a release package installed for product use.
func findNativeInstall(modelsDir, preferredTier string) (nativeInstallPaths, bool) {
	status := managedqwen.InspectNative(modelsDir, preferredTier)
	if !status.Installed {
		return nativeInstallPaths{}, false
	}
	return nativeInstallPaths{
		Root:        status.Root,
		Worker:      status.Worker,
		ModelDir:    status.ModelDir,
		BackendHint: status.BackendHint,
	}, true
}

// startNativeQwenSession launches qwen3-tts-worker against modelDir.
// tier selects the GGUF when the package is multi-tier (QWEN3_TTS_TIER); empty
// uses DefaultModelTier so multi-tier packages do not silently pick the wrong size.
func startNativeQwenSession(ctx context.Context, workerBin, modelDir, tier string, timeout time.Duration) (*nativeQwenSession, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	workerBin, err := filepath.Abs(workerBin)
	if err != nil {
		return nil, fmt.Errorf("resolve native worker: %w", err)
	}
	modelDir, err = filepath.Abs(modelDir)
	if err != nil {
		return nil, fmt.Errorf("resolve native model dir: %w", err)
	}
	tier = managedqwen.NormalizeModelTier(tier)
	// Lifetime is owned by the session (Background), not the startup ctx.
	// CommandContext is required because ConfigureWorkerCommand sets Cancel.
	cmd := exec.CommandContext(context.Background(), workerBin, modelDir)
	managedqwen.ConfigureWorkerCommand(cmd)
	libDir := filepath.Dir(workerBin)
	cmd.Env = withNativeWorkerEnv(os.Environ(), libDir, tier)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("native worker stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, fmt.Errorf("native worker stdout: %w", err)
	}
	stderr := &limitedBuffer{limit: maxWorkerOutput}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		return nil, fmt.Errorf("start native worker: %w", err)
	}
	s := &nativeQwenSession{
		speechSession: &speechSession{
			conn:           newSpeechConn(nativeWorkerLabel, nativeWorkerProtocol, stdout, stdin, stdin),
			label:          nativeWorkerLabel,
			referenceAudio: true,
			rate:           qwen3TTSSampleRate,
		},
		cmd:    cmd,
		stderr: stderr,
		wait:   make(chan error, 1), modelDir: modelDir, protocol: nativeWorkerProtocol,
	}
	s.conn.abort = s.wait
	s.conn.abortErr = s.exitError
	go func() { s.wait <- cmd.Wait() }()

	if timeout <= 0 {
		timeout = defaultQwenTTSTimeout
	}
	readyCh := make(chan speechReadyMsg, 1)
	errCh := make(chan error, 1)
	go func() {
		// Blocking read: the select below owns the timeout and the exit race,
		// so this must not consume s.wait itself.
		line, err := s.conn.ReadLineBlocking()
		if err != nil {
			errCh <- err
			return
		}
		ready, err := s.conn.ParseReady(line)
		if err != nil {
			errCh <- err
			return
		}
		readyCh <- ready
	}()

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case ready := <-readyCh:
		if err := s.conn.ValidateReady(ready); err != nil {
			s.Close()
			if errors.Is(err, errNotReady) {
				return nil, fmt.Errorf("%w%s", err, workerOutputSuffix(stderr.String(), ""))
			}
			return nil, err
		}
		s.streaming = ready.Streaming
		s.capable = ready.streamCapable()
		s.presets = append([]string(nil), ready.Presets...)
		if len(s.presets) == 0 {
			s.presets = loadNativePresetsFromDisk(modelDir)
		}
		return s, nil
	case err := <-errCh:
		s.Close()
		return nil, fmt.Errorf("native worker ready: %w%s", err, workerOutputSuffix(stderr.String(), ""))
	case err := <-s.wait:
		return nil, fmt.Errorf("native worker exited during startup: %v%s", err, workerOutputSuffix(stderr.String(), ""))
	case <-timer.C:
		s.Close()
		return nil, fmt.Errorf("native worker startup timed out after %s%s", timeout, workerOutputSuffix(stderr.String(), ""))
	case <-ctx.Done():
		s.Close()
		return nil, ctx.Err()
	}
}

// withNativeWorkerEnv is the worker's environment contract, owned by the qwen
// package so speech-serve spawns its child exactly as the provider does.
func withNativeWorkerEnv(env []string, libDir, tier string) []string {
	return managedqwen.NativeWorkerEnv(env, libDir, tier)
}

func loadNativePresetsFromDisk(modelDir string) []string {
	path := filepath.Join(modelDir, "presets", "presets.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var doc struct {
		Voices []struct {
			Name string `json:"name"`
		} `json:"voices"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil
	}
	names := make([]string, 0, len(doc.Voices))
	for _, v := range doc.Voices {
		if n := strings.TrimSpace(v.Name); n != "" {
			names = append(names, n)
		}
	}
	return names
}

// exitError renders a child exit for a read that was waiting on it.
func (s *nativeQwenSession) exitError(err error) error {
	return fmt.Errorf("native worker exited: %v%s", err, workerOutputSuffix(s.stderr.String(), ""))
}

// Close shuts down the worker process.
func (s *nativeQwenSession) Close() {
	if !s.beginClose() {
		return
	}
	if s.conn != nil {
		_ = s.conn.Close()
	}
	if s.cmd != nil && s.cmd.Process != nil {
		done := make(chan struct{})
		go func() {
			select {
			case <-s.wait:
			case <-time.After(3 * time.Second):
				_ = s.cmd.Process.Kill()
				<-s.wait
			}
			close(done)
		}()
		<-done
	}
}
