package audiojob

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
)

func waveformFixture(t *testing.T, mode string) (Config, Request, Provenance) {
	t.Helper()
	c, r := fixture(t)
	c.Runtime = Runtime{Provider: "openvoice", Model: "openvoice-v2", Voice: "reference"}
	r.Runtime = c.Runtime
	r.Protocol = ConversionProtocol
	r.Task = "voice_conversion"
	r.Script = ""
	r.Input.SampleRate = WaveformRate
	r.Input.SampleCount = WaveformRate*21 + 17
	r.Input.SourceOriginSamples = WaveformRate * 2
	if err := audio.WriteWAVFloat32(r.Input.Path, WaveformRate, make([]float32, r.Input.SampleCount)); err != nil {
		t.Fatal(err)
	}
	r.Input.SHA256, _ = fileHash(t.Context(), r.Input.Path)
	reference := filepath.Join(c.StorageRoot, "reference.wav")
	if err := audio.WriteWAVFloat32(reference, WaveformRate, make([]float32, WaveformRate)); err != nil {
		t.Fatal(err)
	}
	refHash, _ := fileHash(t.Context(), reference)
	r.Reference = &Input{Path: reference, SHA256: refHash, SampleCount: WaveformRate, SampleRate: WaveformRate, Channels: 1, ChannelRole: "reference"}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	copy := filepath.Join(c.StorageRoot, "native-waveform-fixture")
	if err = os.Link(exe, copy); err != nil {
		data, e := os.ReadFile(exe)
		if e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile(copy, data, 0700); e != nil {
			t.Fatal(e)
		}
	}
	asset := func(name, content string) ManagedFile {
		path := filepath.Join(c.StorageRoot, name)
		if content != "" {
			if err := os.WriteFile(path, []byte(content), 0600); err != nil {
				t.Fatal(err)
			}
		}
		sha, err := fileHash(t.Context(), path)
		if err != nil {
			t.Fatal(err)
		}
		return ManagedFile{Path: path, SHA256: sha}
	}
	c.Waveform = &WaveformConfig{Executable: asset("native-waveform-fixture", ""), Encoder: asset("tone_extract.onnx", "encoder"), Converter: asset("tone_color.onnx", mode), RuntimeLibrary: asset("libonnxruntime.1.30.0.dylib", "library"), Version: WaveformVersion}
	p := Provenance{RuntimeVersion: WaveformVersion, ModelDigest: c.Waveform.Converter.SHA256, EncoderDigest: c.Waveform.Encoder.SHA256, RuntimeDigest: c.Waveform.RuntimeLibrary.SHA256, WorkerDigest: c.Waveform.Executable.SHA256}
	return c, r, p
}
func runWaveformFixture() {
	if os.Getenv("HTTP_PROXY") != "" || os.Getenv("OLLAMA_HOST") != "" || os.Getenv("HF_TOKEN") != "" {
		os.Exit(9)
	}
	root := filepath.Dir(os.Args[0])
	hash := func(name string) string {
		s, e := fileHash(context.Background(), filepath.Join(root, name))
		if e != nil {
			panic(e)
		}
		return s
	}
	if len(os.Args) == 2 && os.Args[1] == "--version" {
		_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"protocol": WaveformWorkerProtocol, "version": WaveformVersion, "model_digest": hash("tone_color.onnx"), "encoder_digest": hash("tone_extract.onnx"), "runtime_digest": hash("libonnxruntime.1.30.0.dylib"), "sample_rate_hz": WaveformRate, "channels": 1, "transport": "waveform_conversion", "chunk_policy": WaveformChunkPolicy, "quality_qualified": false, "platform_supported": true})
		return
	}
	if len(os.Args) != 7 || os.Args[1] != "convert" {
		os.Exit(8)
	}
	// Reproduce native runtime side effects seen in the offline macOS proof.
	if err := os.WriteFile("native-debug.log", []byte("private diagnostics"), 0600); err != nil {
		panic(err)
	}
	if err := os.MkdirAll(filepath.Join(os.Getenv("HOME"), "Library", "Caches"), 0700); err != nil {
		panic(err)
	}
	mode, _ := os.ReadFile(filepath.Join(root, "tone_color.onnx"))
	if string(mode) == "hang" {
		for {
			time.Sleep(time.Second)
		}
	}
	if string(mode) == "flood" {
		for {
			fmt.Print(strings.Repeat("x", 8192))
		}
	}
	data, err := os.ReadFile(os.Args[3])
	if err != nil {
		panic(err)
	}
	samples := make([]float32, len(data)/4)
	for i := range samples {
		samples[i] = math.Float32frombits(binary.LittleEndian.Uint32(data[i*4:])) * .5
	}
	if string(mode) == "short" {
		samples = samples[:len(samples)-1]
	}
	if err = audio.WriteWAVFloat32(os.Args[5], WaveformRate, samples); err != nil {
		panic(err)
	}
	source, _ := fileHash(context.Background(), os.Args[3])
	ref, _ := fileHash(context.Background(), os.Args[4])
	out, _ := fileHash(context.Background(), os.Args[5])
	if string(mode) == "wrong-reference" {
		ref = strings.Repeat("0", 64)
	}
	_ = json.NewEncoder(os.Stdout).Encode(NativeReceipt{Protocol: WaveformWorkerProtocol, Version: WaveformVersion, SourceSHA256: source, ReferenceSHA256: ref, OutputSHA256: out, Samples: int64(len(samples)), Rate: WaveformRate, Transport: "waveform_conversion", ChunkPolicy: WaveformChunkPolicy})
	if string(mode) == "trailing" {
		fmt.Println("{}")
	}
}
func TestConversionNativeProcessPublishesWholeRangeAndIndependentReference(t *testing.T) {
	c, r, _ := waveformFixture(t, "ok")
	t.Setenv("HTTP_PROXY", "unwanted")
	t.Setenv("HF_TOKEN", "unwanted")
	t.Setenv("OLLAMA_HOST", "unwanted")
	worker, p, err := PrepareWaveform(t.Context(), c)
	if err != nil {
		t.Fatal(err)
	}
	var events []Event
	m, err := RunConversion(t.Context(), c, r, worker, p, func(e Event) error { events = append(events, e); return nil })
	if err != nil {
		t.Fatal(err)
	}
	if m.Output.SampleCount != r.Input.SampleCount || m.Output.SampleRate != WaveformRate || m.Reference == nil || *m.Reference != *r.Reference || m.Source != r.Source || m.WorkerDigest != p.WorkerDigest || m.Transport != "waveform_conversion" {
		t.Fatal("lost waveform provenance", m)
	}
	if len(m.SourceWindows) != 3 || m.SourceWindows[0].StartSample != r.Input.SourceOriginSamples || m.SourceWindows[2].EndSample != r.Input.SourceOriginSamples+r.Input.SampleCount {
		t.Fatal("lost source coverage", m.SourceWindows)
	}
	for i, kind := range []string{"accepted", "progress", "artifact", "completed"} {
		if events[i].Type != kind || events[i].Seq != i {
			t.Fatal(events)
		}
	}
	if events[1].Stage != "waveform_conversion" {
		t.Fatal(events)
	}
	entries, err := os.ReadDir(r.OutputDir)
	if err != nil || len(entries) != 2 {
		t.Fatal("private PCM leaked into accepted artifact", entries, err)
	}
	caps := WaveformCapabilities(c.Runtime, p)
	if len(caps.Tasks) != 1 || caps.Tasks[0].Qualified || !caps.Tasks[0].Supported {
		t.Fatal(caps)
	}
}
func TestConversionRejectsTaskSubstitutionAndBadReferenceBeforeWork(t *testing.T) {
	for _, kind := range []string{"mixed", "script", "wrong-task", "missing-reference", "long-reference", "reference-hash", "source-count", "output-budget", "origin", "pack-provider"} {
		t.Run(kind, func(t *testing.T) {
			c, r, p := waveformFixture(t, "ok")
			switch kind {
			case "mixed":
				r.Input.ChannelRole = "mixed"
			case "script":
				r.Script = "replace words"
			case "wrong-task":
				r.Task = "dubbing"
			case "missing-reference":
				r.Reference = nil
			case "long-reference":
				r.Reference.SampleCount = WaveformRate * 16
			case "reference-hash":
				r.Reference.SHA256 = strings.Repeat("0", 64)
			case "source-count":
				r.Input.SampleCount++
			case "output-budget":
				r.Limits.OutputBytes = 44
			case "origin":
				r.Reference.SourceOriginSamples = 1
			case "pack-provider":
				c.Runtime.Provider = "ollama"
				r.Runtime = c.Runtime
			}
			called := false
			_, err := RunConversion(t.Context(), c, r, conversionFunc(func(context.Context, string, string, string, int) (NativeReceipt, error) {
				called = true
				return NativeReceipt{}, nil
			}), p, func(Event) error { return nil })
			if err == nil || called {
				t.Fatal("invalid conversion reached worker", err)
			}
		})
	}
}

type conversionFunc func(context.Context, string, string, string, int) (NativeReceipt, error)

func (f conversionFunc) Convert(ctx context.Context, s, r, o string, n int) (NativeReceipt, error) {
	return f(ctx, s, r, o, n)
}
func TestConversionNativeFailureCancellationAndOutputOverflowPublishNothing(t *testing.T) {
	for _, mode := range []string{"short", "wrong-reference", "trailing", "hang", "flood", "reference-changed"} {
		t.Run(mode, func(t *testing.T) {
			c, r, _ := waveformFixture(t, mode)
			worker, p, err := PrepareWaveform(t.Context(), c)
			if err != nil {
				t.Fatal(err)
			}
			limit := 5 * time.Second
			if mode == "hang" || mode == "flood" {
				limit = 350 * time.Millisecond
			}
			ctx, cancel := context.WithTimeout(t.Context(), limit)
			defer cancel()
			var convert WaveformConverter = worker
			if mode == "reference-changed" {
				convert = conversionFunc(func(ctx context.Context, s, ref, out string, timeout int) (NativeReceipt, error) {
					receipt, err := worker.Convert(ctx, s, ref, out, timeout)
					if err == nil {
						err = os.WriteFile(r.Reference.Path, []byte("changed"), 0600)
					}
					return receipt, err
				})
			}
			_, err = RunConversion(ctx, c, r, convert, p, func(Event) error { return nil })
			if err == nil {
				t.Fatal("accepted failed native job")
			}
			if mode != "hang" && mode != "flood" && (errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled)) {
				t.Fatal("fixture timed out without exercising bad output", err)
			}
			if _, err = os.Lstat(r.OutputDir); !os.IsNotExist(err) {
				t.Fatal("partial output published", err)
			}
			for _, pattern := range []string{".audio-job-*", "*.lock", ".waveform-version-*"} {
				matches, _ := filepath.Glob(filepath.Join(c.StorageRoot, pattern))
				if len(matches) > 0 {
					t.Fatal("owned artifacts leaked", matches)
				}
			}
		})
	}
}
