package audiojob

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"
)

type NativeReceipt struct {
	Protocol        string `json:"protocol"`
	Version         string `json:"version"`
	SourceSHA256    string `json:"source_sha256"`
	ReferenceSHA256 string `json:"reference_sha256"`
	OutputSHA256    string `json:"output_sha256"`
	Samples         int64  `json:"samples"`
	Rate            int    `json:"sample_rate_hz"`
	Transport       string `json:"transport"`
	ChunkPolicy     string `json:"chunk_policy"`
	Qualified       bool   `json:"quality_qualified"`
}
type WaveformConverter interface {
	Convert(context.Context, string, string, string, int) (NativeReceipt, error)
}
type NativeConverter struct{ pack WaveformConfig }
type boundedNativeOutput struct {
	bytes.Buffer
	limit  int
	cancel context.CancelFunc
}

func (b *boundedNativeOutput) Write(p []byte) (int, error) {
	if len(p) > b.limit-b.Len() {
		b.cancel()
		return 0, fmt.Errorf("native worker output exceeded limit")
	}
	return b.Buffer.Write(p)
}
func nativeExecute(ctx context.Context, exe, scratch string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	command := exec.CommandContext(ctx, exe, args...)
	command.Env = managedEnvironment(scratch)
	command.Dir = scratch
	command.WaitDelay = time.Second
	configureWaveformCancellation(command)
	stdout := &boundedNativeOutput{limit: 64 << 10, cancel: cancel}
	stderr := &boundedNativeOutput{limit: 64 << 10, cancel: cancel}
	command.Stdout = stdout
	command.Stderr = stderr
	if err := command.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("native waveform worker failed: %w: %s", err, stderr.String())
	}
	return stdout.Bytes(), nil
}
func decodeNative(data []byte, out any) error {
	d := json.NewDecoder(bytes.NewReader(data))
	if err := d.Decode(out); err != nil {
		return err
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return fmt.Errorf("native worker returned trailing receipt data")
	}
	return nil
}
func PrepareWaveform(ctx context.Context, c Config) (*NativeConverter, Provenance, error) {
	if err := c.Validate(); err != nil {
		return nil, Provenance{}, err
	}
	if c.Waveform == nil {
		return nil, Provenance{}, fmt.Errorf("explicit waveform pack required")
	}
	if err := c.Waveform.verify(ctx); err != nil {
		return nil, Provenance{}, err
	}
	scratch, err := os.MkdirTemp(c.StorageRoot, ".waveform-version-")
	if err != nil {
		return nil, Provenance{}, err
	}
	defer os.RemoveAll(scratch)
	limited, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	data, err := nativeExecute(limited, c.Waveform.Executable.Path, scratch, "--version")
	if err != nil {
		return nil, Provenance{}, err
	}
	var v struct {
		Protocol          string `json:"protocol"`
		Version           string `json:"version"`
		Model             string `json:"model_digest"`
		Encoder           string `json:"encoder_digest"`
		Runtime           string `json:"runtime_digest"`
		Rate              int    `json:"sample_rate_hz"`
		Channels          int    `json:"channels"`
		Transport         string `json:"transport"`
		ChunkPolicy       string `json:"chunk_policy"`
		Qualified         bool   `json:"quality_qualified"`
		PlatformSupported bool   `json:"platform_supported"`
	}
	if err := decodeNative(data, &v); err != nil {
		return nil, Provenance{}, err
	}
	p := Provenance{RuntimeVersion: v.Version, ModelDigest: v.Model, EncoderDigest: v.Encoder, RuntimeDigest: v.Runtime, WorkerDigest: c.Waveform.Executable.SHA256}
	if v.Protocol != WaveformWorkerProtocol || !v.PlatformSupported || v.Rate != WaveformRate || v.Channels != 1 || v.Transport != "waveform_conversion" || v.ChunkPolicy != WaveformChunkPolicy || v.Qualified || !c.Waveform.matches(p) {
		return nil, Provenance{}, fmt.Errorf("native waveform worker/version/platform does not match selected pack")
	}
	return &NativeConverter{pack: *c.Waveform}, p, nil
}
func (n *NativeConverter) Convert(ctx context.Context, source, reference, output string, timeout int) (NativeReceipt, error) {
	var receipt NativeReceipt
	if err := n.pack.verify(ctx); err != nil {
		return receipt, err
	}
	// Native libraries can write caches and diagnostics under HOME or cwd.
	// Keep those outside the artifact directory's published contents.
	scratch, err := os.MkdirTemp(filepath.Dir(output), ".native-runtime-")
	if err != nil {
		return receipt, err
	}
	defer os.RemoveAll(scratch)
	data, err := nativeExecute(ctx, n.pack.Executable.Path, scratch, "convert", filepath.Dir(n.pack.Encoder.Path), source, reference, output, strconv.Itoa(timeout*1000))
	if err != nil {
		return receipt, err
	}
	err = decodeNative(data, &receipt)
	return receipt, err
}
