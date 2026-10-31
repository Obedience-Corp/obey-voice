package audiojob

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
)

func RunConversion(ctx context.Context, c Config, r Request, converter WaveformConverter, p Provenance, emit func(Event) error) (Manifest, error) {
	return run(ctx, c, r, p, emit, func(ctx context.Context, stage string) (Manifest, error) {
		return renderConversion(ctx, r, converter, p, stage)
	})
}
func rawConversionInput(ctx context.Context, in Input, path string) (string, error) {
	if err := checkInput(ctx, in); err != nil {
		return "", err
	}
	input, err := os.Open(in.Path)
	if err != nil {
		return "", err
	}
	defer input.Close()
	info, err := input.Stat()
	if err != nil {
		return "", err
	}
	format, err := audio.ReadWAVFormat(input, info.Size())
	if err != nil {
		return "", err
	}
	if _, err = input.Seek(format.DataOffset, io.SeekStart); err != nil {
		return "", err
	}
	out, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return "", err
	}
	defer out.Close()
	remaining := in.SampleCount
	pcm := make([]byte, 32768)
	floats := make([]byte, 65536)
	for remaining > 0 {
		if err = ctx.Err(); err != nil {
			return "", err
		}
		count := min(remaining, int64(len(pcm)/2))
		if _, err = io.ReadFull(input, pcm[:count*2]); err != nil {
			return "", err
		}
		for i := int64(0); i < count; i++ {
			v := float32(int16(binary.LittleEndian.Uint16(pcm[i*2:]))) / 32768
			binary.LittleEndian.PutUint32(floats[i*4:], math.Float32bits(v))
		}
		if _, err = out.Write(floats[:count*4]); err != nil {
			return "", err
		}
		remaining -= count
	}
	if err = out.Close(); err != nil {
		return "", err
	}
	if err = checkInput(ctx, in); err != nil {
		return "", err
	}
	return fileHash(ctx, path)
}
func renderConversion(ctx context.Context, r Request, converter WaveformConverter, p Provenance, stage string) (Manifest, error) {
	var manifest Manifest
	if r.Protocol != ConversionProtocol || r.Reference == nil || converter == nil {
		return manifest, fmt.Errorf("explicit v4 conversion and reference required")
	}
	source := filepath.Join(stage, "source.f32")
	reference := filepath.Join(stage, "reference.f32")
	defer os.Remove(source)
	defer os.Remove(reference)
	sourceHash, err := rawConversionInput(ctx, r.Input, source)
	if err != nil {
		return manifest, err
	}
	referenceHash, err := rawConversionInput(ctx, *r.Reference, reference)
	if err != nil {
		return manifest, err
	}
	output := filepath.Join(stage, "audio.wav")
	receipt, err := converter.Convert(ctx, source, reference, output, r.Limits.TimeoutSeconds)
	if err != nil {
		return manifest, err
	}
	if err = ctx.Err(); err != nil {
		return manifest, err
	}
	if receipt.Protocol != WaveformWorkerProtocol || receipt.Version != WaveformVersion || receipt.SourceSHA256 != sourceHash || receipt.ReferenceSHA256 != referenceHash || receipt.Samples != r.Input.SampleCount || receipt.Rate != WaveformRate || receipt.Transport != "waveform_conversion" || receipt.ChunkPolicy != WaveformChunkPolicy || receipt.Qualified {
		return manifest, fmt.Errorf("waveform receipt does not match source, reference, geometry or transport")
	}
	info, err := os.Lstat(output)
	if err != nil {
		return manifest, err
	}
	if !info.Mode().IsRegular() || info.Size() > r.Limits.OutputBytes || info.Size() != 44+2*r.Input.SampleCount {
		return manifest, fmt.Errorf("converted WAV is partial, nonregular or exceeds output budget")
	}
	f, err := os.Open(output)
	if err != nil {
		return manifest, err
	}
	format, err := audio.ReadWAVFormat(f, info.Size())
	_ = f.Close()
	if err != nil {
		return manifest, err
	}
	if format.SampleRate != WaveformRate || format.Channels != 1 || format.BitsPerSample != 16 || format.Frames() != r.Input.SampleCount {
		return manifest, fmt.Errorf("converted audio changed the recording sample count or format")
	}
	hash, err := fileHash(ctx, output)
	if err != nil {
		return manifest, err
	}
	if !HashValid(receipt.OutputSHA256) || hash != receipt.OutputSHA256 {
		return manifest, fmt.Errorf("converted audio hash differs from native receipt")
	}
	windows := make([]Window, 0, (r.Input.SampleCount+WaveformRate*10-1)/(WaveformRate*10))
	for start := int64(0); start < r.Input.SampleCount; start += WaveformRate * 10 {
		windows = append(windows, Window{StartSample: r.Input.SourceOriginSamples + start, EndSample: r.Input.SourceOriginSamples + min(start+WaveformRate*10, r.Input.SampleCount)})
	}
	manifest = Manifest{Protocol: r.Protocol, JobID: r.JobID, Task: r.Task, Source: r.Source, Input: r.Input, Reference: r.Reference, Runtime: r.Runtime, RuntimeVersion: p.RuntimeVersion, ModelDigest: p.ModelDigest, EncoderDigest: p.EncoderDigest, RuntimeDigest: p.RuntimeDigest, WorkerDigest: p.WorkerDigest, Transport: "waveform_conversion", Language: r.Language, DurationPolicy: r.DurationPolicy, ChunkPolicy: WaveformChunkPolicy, ConversionSampling: &ConversionSampling{Temperature: 0}, SourceWindows: windows, Output: Output{Path: "audio.wav", SHA256: hash, SampleRate: WaveformRate, Channels: 1, SampleCount: r.Input.SampleCount}}
	return manifest, nil
}
