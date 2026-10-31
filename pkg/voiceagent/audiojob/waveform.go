package audiojob

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

const WaveformVersion = "openvoice-v2-onnxruntime-1.30.0"
const WaveformWorkerProtocol = "keepshot.recorded-voice-worker/v1"
const WaveformChunkPolicy = "10s-core-1s-context-linear-crossfade/v1"
const WaveformRate = 22050

type WaveformConfig struct {
	Executable     ManagedFile `json:"executable"`
	Encoder        ManagedFile `json:"encoder"`
	Converter      ManagedFile `json:"converter"`
	RuntimeLibrary ManagedFile `json:"runtime_library"`
	Version        string      `json:"version"`
}
type ConversionSampling struct {
	Temperature float64 `json:"temperature"`
}

func (m WaveformConfig) files() []ManagedFile {
	return []ManagedFile{m.Executable, m.Encoder, m.Converter, m.RuntimeLibrary}
}
func (m WaveformConfig) Validate() error {
	if m.Version != WaveformVersion {
		return fmt.Errorf("waveform conversion requires reviewed runtime %s", WaveformVersion)
	}
	for _, f := range m.files() {
		if !filepath.IsAbs(f.Path) || !HashValid(f.SHA256) {
			return fmt.Errorf("waveform assets require absolute paths and SHA-256")
		}
	}
	root := filepath.Dir(m.Encoder.Path)
	for name, f := range map[string]ManagedFile{"tone_extract.onnx": m.Encoder, "tone_color.onnx": m.Converter, "libonnxruntime.1.30.0.dylib": m.RuntimeLibrary} {
		if filepath.Base(f.Path) != name || filepath.Dir(f.Path) != root {
			return fmt.Errorf("waveform native assets must share one pack with fixed filenames")
		}
	}
	return nil
}
func (m WaveformConfig) verify(ctx context.Context) error {
	if err := m.Validate(); err != nil {
		return err
	}
	for i, f := range m.files() {
		info, err := os.Lstat(f.Path)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || info.Size() == 0 || (i == 0 && info.Mode().Perm()&0111 == 0) {
			return fmt.Errorf("waveform asset must be a nonempty regular file; worker must be executable")
		}
		hash, err := fileHash(ctx, f.Path)
		if err != nil {
			return err
		}
		if hash != f.SHA256 {
			return fmt.Errorf("waveform asset hash changed: repair the selected pack")
		}
	}
	return nil
}
func (m WaveformConfig) matches(p Provenance) bool {
	return p.RuntimeVersion == m.Version && p.ModelDigest == m.Converter.SHA256 && p.EncoderDigest == m.Encoder.SHA256 && p.RuntimeDigest == m.RuntimeLibrary.SHA256 && p.WorkerDigest == m.Executable.SHA256
}
func (r Request) validateConversion(c Config) error {
	if c.Waveform == nil || r.Task != "voice_conversion" || r.Script != "" || r.Instruction != "" || r.TargetLanguage != "" || r.TranslationReview != nil || r.Language != "en" {
		return fmt.Errorf("v4 requires explicit English waveform conversion, without script, translation or instruction")
	}
	if r.Input.SampleCount < WaveformRate || r.Input.SampleCount > WaveformRate*3600 || (r.Input.ChannelRole != "microphone" && r.Input.ChannelRole != "speech") {
		return fmt.Errorf("conversion requires 1 second–1 hour of isolated speech; mixed input needs explicit separation")
	}
	ref := r.Reference
	if ref == nil || !filepath.IsAbs(ref.Path) || !HashValid(ref.SHA256) || ref.SampleCount < WaveformRate || ref.SampleCount > WaveformRate*15 || ref.SampleRate != WaveformRate || ref.Channels != 1 || ref.ChannelRole != "reference" || ref.SourceOriginSamples != 0 {
		return fmt.Errorf("conversion requires an explicit hash-addressed 1–15 second mono 22050Hz reference WAV")
	}
	if r.Limits.OutputBytes < 44+2*r.Input.SampleCount {
		return fmt.Errorf("output budget cannot hold an exact-duration converted WAV")
	}
	return nil
}
func WaveformCapabilities(runtime Runtime, p Provenance) Capabilities {
	return Capabilities{Schema: "obey-audio-capabilities/v1", Runtime: runtime, RuntimeVersion: p.RuntimeVersion, ModelDigest: p.ModelDigest, RuntimeDigest: p.RuntimeDigest, InputFormat: "wav_pcm_s16le", SampleRate: WaveformRate, Channels: 1, ChunkPolicy: WaveformChunkPolicy, Tasks: []Capability{{Task: "voice_conversion", Supported: true, Installed: true, Qualified: false, Reason: "Experimental waveform conversion; intelligibility, voice similarity, delivery and seams remain unqualified.", Protocol: ConversionProtocol, Languages: []string{"en"}, Voices: []string{"reference"}, OutputFormat: "wav_pcm_s16le_22050_mono"}}}
}
