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
	"sync"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	managedqwen "github.com/Obedience-Corp/obey-voice/pkg/voiceagent/qwen"
)

// nativeWorkerProtocol is frozen in projects/qwen3-tts-native/docs/PROTOCOL.md.
const nativeWorkerProtocol = "qwen3-tts-worker/v1"

// nativeWorkerLabel prefixes this transport's protocol errors.
const nativeWorkerLabel = "native worker"

// nativeQwenSession is a long-lived client for bin/qwen3-tts-worker (no Python).
// Stage A: whole-utterance PCM after synth (one pcm_meta + raw f32le + final).
// Stage B will emit multiple pcm_meta chunks; this client already concatenates.
// Framing lives in speechConn so the LAN client can reuse it byte for byte.
type nativeQwenSession struct {
	cmd    *exec.Cmd
	conn   *speechConn
	stderr *limitedBuffer
	wait   chan error

	mu        sync.Mutex
	activeMu  sync.Mutex
	activeID  string
	closed    bool
	request   uint64
	modelDir  string
	protocol  string
	rate      int
	streaming bool
	presets   []string
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
}

// findNativeInstall looks for a release package installed for product use.
func findNativeInstall(modelsDir, preferredTier string) (nativeInstallPaths, bool) {
	status := managedqwen.InspectNative(modelsDir, preferredTier)
	if !status.Installed {
		return nativeInstallPaths{}, false
	}
	return nativeInstallPaths{Root: status.Root, Worker: status.Worker, ModelDir: status.ModelDir}, true
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
	// CommandContext is required because configureQwenCommand sets Cancel.
	cmd := exec.CommandContext(context.Background(), workerBin, modelDir)
	configureQwenCommand(cmd)
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
		cmd:    cmd,
		conn:   newSpeechConn(nativeWorkerLabel, nativeWorkerProtocol, stdout, stdin, stdin),
		stderr: stderr,
		wait:   make(chan error, 1), modelDir: modelDir, protocol: nativeWorkerProtocol,
		rate: qwen3TTSSampleRate,
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

// withNativeWorkerEnv sets DYLD/LD library paths and optional QWEN3_TTS_TIER.
// Parent QWEN3_TTS_TIER / QWEN3_TTS_MODEL are stripped so the host config wins
// and ambient shell env cannot override a fail-closed product tier.
func withNativeWorkerEnv(env []string, libDir, tier string) []string {
	out := make([]string, 0, len(env)+4)
	for _, e := range env {
		key, _, ok := strings.Cut(e, "=")
		if !ok {
			out = append(out, e)
			continue
		}
		switch key {
		case "DYLD_LIBRARY_PATH", "LD_LIBRARY_PATH", "QWEN3_TTS_TIER", "QWEN3_TTS_MODEL":
			continue
		}
		out = append(out, e)
	}
	sep := string(os.PathListSeparator)
	for _, key := range []string{"DYLD_LIBRARY_PATH", "LD_LIBRARY_PATH"} {
		prev := os.Getenv(key)
		if prev != "" {
			out = append(out, key+"="+libDir+sep+prev)
		} else {
			out = append(out, key+"="+libDir)
		}
	}
	if tier = strings.TrimSpace(tier); tier != "" {
		out = append(out, "QWEN3_TTS_TIER="+tier)
	}
	return out
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

// SynthesizeToStream runs one request and writes float32 PCM into stream.
func (s *nativeQwenSession) SynthesizeToStream(ctx context.Context, req SynthesisRequest, stream *audio.PCMStream) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errors.New("native worker is closed")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	s.request++
	id := fmt.Sprintf("nqwen-%d", s.request)

	msg := map[string]string{
		"type": "synthesize",
		"id":   id,
		"text": req.Text,
	}
	if ref := strings.TrimSpace(req.ReferenceAudio); ref != "" {
		msg["ref_wav"] = ref
	}
	// Shipped clones are not model presets. A ref_wav is the throat.
	if _, shipped := managedqwen.LookupShipped(req.Voice); !shipped {
		if preset := strings.TrimSpace(req.Voice); preset != "" && !strings.EqualFold(preset, "default") {
			msg["preset"] = preset
		}
	}
	// The active id is published under the write lock so a concurrent
	// CancelActive cannot name a request the worker has not read yet.
	var aborted error
	err := s.conn.WriteJSONFunc(func() (any, error) {
		if aborted = ctx.Err(); aborted != nil {
			return nil, aborted
		}
		s.activeMu.Lock()
		s.activeID = id
		s.activeMu.Unlock()
		return msg, nil
	})
	if aborted != nil {
		return aborted
	}
	if err != nil {
		s.clearActive(id)
		return fmt.Errorf("write native worker request: %w", err)
	}
	defer s.clearActive(id)

	rateSet := false
	for {
		if err := ctx.Err(); err != nil {
			_ = s.sendCancel(id)
			return err
		}
		line, err := s.readLine(ctx)
		if err != nil {
			return err
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var head struct {
			Type       string `json:"type"`
			ID         string `json:"id"`
			Message    string `json:"message"`
			SampleRate int    `json:"sample_rate"`
			NSamples   int    `json:"n_samples"`
		}
		if err := json.Unmarshal([]byte(line), &head); err != nil {
			return fmt.Errorf("native worker line: %w (%q)", err, line)
		}
		switch head.Type {
		case "error":
			return fmt.Errorf("native worker error: %s", head.Message)
		case "pcm_meta":
			n := head.NSamples
			if n <= 0 {
				return fmt.Errorf("native worker pcm_meta n_samples=%d", n)
			}
			rate := head.SampleRate
			if rate == 0 {
				rate = s.rate
			}
			if rate != qwen3TTSSampleRate {
				return fmt.Errorf("native worker returned %d Hz, want %d Hz", rate, qwen3TTSSampleRate)
			}
			if !rateSet {
				if err := stream.SetSampleRate(rate); err != nil {
					return err
				}
				rateSet = true
			}
			samples, err := s.conn.ReadPCM(n)
			if err != nil {
				return fmt.Errorf("native worker pcm read: %w", err)
			}
			const chunk = 2048
			for start := 0; start < len(samples); start += chunk {
				if err := ctx.Err(); err != nil {
					_ = s.sendCancel(id)
					return err
				}
				end := min(start+chunk, len(samples))
				if err := stream.Write(samples[start:end]); err != nil {
					return err
				}
			}
		case "final":
			if !rateSet {
				if err := stream.SetSampleRate(qwen3TTSSampleRate); err != nil {
					return err
				}
			}
			return nil
		case "generating":
			// progress only
		default:
			// ignore future control types
		}
	}
}

func (s *nativeQwenSession) sendCancel(id string) error {
	return s.conn.WriteJSON(map[string]string{"type": "cancel", "id": id})
}

// CancelActive writes a cancellation for the session-generated request ID
// without waiting for the synthesis/read lock. Stage A workers may only act on
// it between requests, but the write remains prompt and Stage B can interrupt
// mid-synthesis.
func (s *nativeQwenSession) CancelActive() error {
	if s == nil {
		return nil
	}
	// The id is read under the write lock: a synthesize that has published its
	// id but not yet written the line must be waited out, not cancelled blind.
	err := s.conn.WriteJSONFunc(func() (any, error) {
		s.activeMu.Lock()
		id := s.activeID
		s.activeMu.Unlock()
		if id == "" {
			return nil, errNoActiveRequest
		}
		return map[string]string{"type": "cancel", "id": id}, nil
	})
	if errors.Is(err, errNoActiveRequest) {
		return nil
	}
	return err
}

func (s *nativeQwenSession) clearActive(id string) {
	s.activeMu.Lock()
	defer s.activeMu.Unlock()
	if s.activeID == id {
		s.activeID = ""
	}
}

func (s *nativeQwenSession) readLine(ctx context.Context) (string, error) {
	return s.conn.ReadLine(ctx)
}

// exitError renders a child exit for a read that was waiting on it.
func (s *nativeQwenSession) exitError(err error) error {
	return fmt.Errorf("native worker exited: %v%s", err, workerOutputSuffix(s.stderr.String(), ""))
}

// Close shuts down the worker process.
func (s *nativeQwenSession) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.closed = true
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
