//go:build !integration

package cmd

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/transcription/protocol"
)

// transcribeBinaryDir holds the built CLI. No test owns it, so TestMain removes
// it; without that every run of this package left a 45 MB folder in the temp
// directory.
var transcribeBinaryDir string

func TestMain(m *testing.M) {
	code := m.Run()
	if transcribeBinaryDir != "" {
		_ = os.RemoveAll(transcribeBinaryDir)
	}
	os.Exit(code)
}

// buildTranscribeBinary builds the real CLI once per test binary. Everything
// the process-level scenarios assert (stdout is only JSONL, diagnostics are on
// stderr, the exit status) is a property of the process, not of a function.
var buildTranscribeBinary = sync.OnceValues(func() (string, error) {
	dir, err := os.MkdirTemp("", "obey-voice-transcribe-bin")
	if err != nil {
		return "", err
	}
	transcribeBinaryDir = dir
	out := filepath.Join(dir, "obey-voice")
	cmd := exec.Command("go", "build", "-o", out, "github.com/Obedience-Corp/obey-voice/cmd/obey-voice")
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return "", err
	}
	return out, nil
})

// transcribeProcessEnv is an isolated HOME and models directory. The real
// ~/.obey and ~/.cache are never read: HOME decides the config directory and
// MODELS_DIR is the bound name for models_dir.
func transcribeProcessEnv(t *testing.T) (home, models string, env []string) {
	t.Helper()
	root := t.TempDir()
	home = filepath.Join(root, "home")
	models = filepath.Join(root, "models")
	for _, dir := range []string{home, models} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return home, models, append(os.Environ(), "HOME="+home, "MODELS_DIR="+models)
}

// writeTranscribeWAV writes a silent PCM16 WAV with the given geometry. The
// rate, channel count and length are all parameters, because the mismatch
// cases need a header that disagrees with the request.
func writeTranscribeWAV(t *testing.T, path string, sampleRate, channels, frames int) {
	t.Helper()
	data := make([]byte, frames*channels*2)
	var buf bytes.Buffer
	buf.WriteString("RIFF")
	_ = binary.Write(&buf, binary.LittleEndian, uint32(36+len(data)))
	buf.WriteString("WAVEfmt ")
	_ = binary.Write(&buf, binary.LittleEndian, uint32(16))
	_ = binary.Write(&buf, binary.LittleEndian, uint16(1))
	_ = binary.Write(&buf, binary.LittleEndian, uint16(channels))
	_ = binary.Write(&buf, binary.LittleEndian, uint32(sampleRate))
	_ = binary.Write(&buf, binary.LittleEndian, uint32(sampleRate*channels*2))
	_ = binary.Write(&buf, binary.LittleEndian, uint16(channels*2))
	_ = binary.Write(&buf, binary.LittleEndian, uint16(16))
	buf.WriteString("data")
	_ = binary.Write(&buf, binary.LittleEndian, uint32(len(data)))
	buf.Write(data)
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeProcessRequest(t *testing.T, dir, jobID, inputPath string, overrides map[string]any) string {
	t.Helper()
	req := map[string]any{
		"protocol": protocol.Protocol,
		"job_id":   jobID,
		"input": map[string]any{
			"path":               inputPath,
			"format":             protocol.FormatWAVPCMS16LE,
			"sample_rate_hz":     16000,
			"channels":           1,
			"timeline_origin_us": 0,
		},
		"timing": protocol.TimingUtterance,
	}
	for k, v := range overrides {
		req[k] = v
	}
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, jobID+".json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

type processResult struct {
	stdout string
	stderr string
	exit   int
}

func runTranscribeProcess(t *testing.T, env []string, args ...string) processResult {
	t.Helper()
	bin, err := buildTranscribeBinary()
	if err != nil {
		t.Fatalf("build the CLI: %v", err)
	}
	cmd := exec.Command(bin, args...)
	cmd.Env = env
	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	err = cmd.Run()
	exit := 0
	if exitErr, ok := errors.AsType[*exec.ExitError](err); ok {
		exit = exitErr.ExitCode()
	} else if err != nil {
		t.Fatalf("run the CLI: %v", err)
	}
	return processResult{stdout: out.String(), stderr: errBuf.String(), exit: exit}
}

func TestTranscribeProcessModelFreeScenarios(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the CLI")
	}
	_, _, env := transcribeProcessEnv(t)
	dir := t.TempDir()

	good := filepath.Join(dir, "good.wav")
	writeTranscribeWAV(t, good, 16000, 1, 16000)
	fast := filepath.Join(dir, "fast.wav")
	writeTranscribeWAV(t, fast, 48000, 1, 16000)

	broken := filepath.Join(dir, "broken.json")
	if err := os.WriteFile(broken, []byte("{ not json"), 0o600); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name    string
		request string
		jobID   string
		exit    int
		code    string
		stream  bool
	}{
		{
			name:    "unsupported protocol version",
			request: writeProcessRequest(t, dir, "p-bad-version", good, map[string]any{"protocol": "obey-transcript/v3"}),
			jobID:   "p-bad-version",
			exit:    protocol.ExitInvalid,
			code:    protocol.CodeUnsupportedProtocol,
			stream:  true,
		},
		{
			name:    "input missing",
			request: writeProcessRequest(t, dir, "p-missing-input", filepath.Join(dir, "nope.wav"), nil),
			jobID:   "p-missing-input",
			exit:    protocol.ExitInvalid,
			code:    protocol.CodeInputUnreadable,
			stream:  true,
		},
		{
			name:    "header disagrees with the request",
			request: writeProcessRequest(t, dir, "p-wrong-rate", fast, nil),
			jobID:   "p-wrong-rate",
			exit:    protocol.ExitInvalid,
			code:    protocol.CodeInputFormatMismatch,
			stream:  true,
		},
		{
			name:    "models are not installed",
			request: writeProcessRequest(t, dir, "p-model-missing", good, nil),
			jobID:   "p-model-missing",
			exit:    protocol.ExitUnavailable,
			code:    protocol.CodeModelMissing,
			stream:  true,
		},
		{
			name:    "request is not json",
			request: broken,
			exit:    protocol.ExitInvalid,
			stream:  false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := runTranscribeProcess(t, env, "transcribe", "--request", tc.request, "--jsonl")

			if res.exit != tc.exit {
				t.Fatalf("exit = %d, want %d\nstdout: %s\nstderr: %s", res.exit, tc.exit, res.stdout, res.stderr)
			}
			if res.stderr == "" {
				t.Error("stderr carries no diagnostic")
			}
			if !tc.stream {
				if res.stdout != "" {
					t.Fatalf("stdout carries %q; a request with no job ID has no stream", res.stdout)
				}
				return
			}
			events := decodeStream(t, res.stdout, tc.jobID)
			failed, ok := events[len(events)-1].(*protocol.Failed)
			if !ok {
				t.Fatalf("terminal event is %s, want failed", events[len(events)-1].Type())
			}
			if failed.Code != tc.code {
				t.Fatalf("code = %s, want %s (%s)", failed.Code, tc.code, failed.Message)
			}
		})
	}
}

func TestTranscribeProcessCapabilitiesIsJSONOnStdout(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the CLI")
	}
	_, models, env := transcribeProcessEnv(t)

	res := runTranscribeProcess(t, env, "transcribe", "capabilities", "--json")
	if res.exit != 0 {
		t.Fatalf("exit = %d\nstderr: %s", res.exit, res.stderr)
	}
	var doc protocol.Capabilities
	if err := json.Unmarshal([]byte(res.stdout), &doc); err != nil {
		t.Fatalf("stdout is not the capabilities document: %v\n%s", err, res.stdout)
	}
	if doc.Schema != protocol.CapabilitiesSchema {
		t.Errorf("schema = %q", doc.Schema)
	}
	if doc.Readiness.ModelsDir != models {
		t.Errorf("models_dir = %q, want the isolated %q", doc.Readiness.ModelsDir, models)
	}
	if doc.Readiness.Ready {
		t.Error("ready is true with an empty models directory")
	}
}

// syncBuffer is a bytes.Buffer whose Write and String take the same lock.
// The SIGINT test polls stdout while exec's copy goroutine writes into it.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// TestTranscribeProcessCancellation needs a model. There is no model-free
// place where the process waits: everything before the decoder is a
// filesystem read.
//
// The command opens the WAV and walks its header through a seekable source
// before it looks at any model. A model-free run reaches a terminal event in
// microseconds and never sits in a place where a signal would change the
// outcome.
//
// A FIFO input does not help. It is not seekable and Stat reports size 0, so
// the header walk fails immediately and the process exits 2 in milliseconds.
// Worse, os.Open on a FIFO with no writer blocks inside a syscall, so a SIGINT
// there is not handled by the command at all: it hits the signal context
// force-quit, which is outside the 2 s budget and emits no cancelled event.
//
// Deterministic, always-on coverage is cancellation at command level, with a
// fake runner that cancels its own context:
// TestTranscribeCancellationEmitsCancelledAndExits130. This test is the
// env-gated real SIGINT to the real process, mid-decode, with a real model.
func TestTranscribeProcessCancellation(t *testing.T) {
	modelsDir := strings.TrimSpace(os.Getenv("OBEY_VOICE_TRANSCRIBE_MODELS_DIR"))
	fixture := strings.TrimSpace(os.Getenv("OBEY_VOICE_TRANSCRIBE_LONG_WAV"))
	if modelsDir == "" || fixture == "" {
		t.Skip("set OBEY_VOICE_TRANSCRIBE_MODELS_DIR and OBEY_VOICE_TRANSCRIBE_LONG_WAV (a 16 kHz mono WAV of at least 60 s) for the SIGINT proof")
	}
	bin, err := buildTranscribeBinary()
	if err != nil {
		t.Fatalf("build the CLI: %v", err)
	}
	home := filepath.Join(t.TempDir(), "home")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	request := writeProcessRequest(t, t.TempDir(), "p-cancel", fixture, nil)

	cmd := exec.Command(bin, "transcribe", "--request", request, "--jsonl")
	cmd.Env = append(os.Environ(), "HOME="+home, "MODELS_DIR="+modelsDir)
	var out, errBuf syncBuffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}

	// Wait for the accepted event, so the signal lands inside the run rather
	// than during model load.
	deadline := time.Now().Add(60 * time.Second)
	for !strings.Contains(out.String(), `"type":"accepted"`) {
		if time.Until(deadline) <= 0 {
			_ = cmd.Process.Kill()
			t.Fatalf("no accepted event within 60 s\nstderr: %s", errBuf.String())
		}
		time.Sleep(50 * time.Millisecond)
	}

	sent := time.Now()
	if err := cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	err = cmd.Wait()
	elapsed := time.Since(sent)

	exit := 0
	if exitErr, ok := errors.AsType[*exec.ExitError](err); ok {
		exit = exitErr.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	t.Logf("SIGINT to exit: %s", elapsed)
	if exit != protocol.ExitCancelled {
		t.Fatalf("exit = %d, want %d\nstderr: %s", exit, protocol.ExitCancelled, errBuf.String())
	}
	if elapsed > 2*time.Second {
		t.Errorf("took %s to stop, the budget is 2 s", elapsed)
	}
	events := decodeStream(t, out.String(), "p-cancel")
	if _, ok := events[len(events)-1].(*protocol.Cancelled); !ok {
		t.Fatalf("terminal event is %s, want cancelled", events[len(events)-1].Type())
	}
}
