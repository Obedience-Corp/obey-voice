package audiojob

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
)

func renderText(ctx context.Context, r Request, s TextAnalyzer, p Provenance, stage string) (Manifest, error) {
	var manifest Manifest
	if r.Protocol != TextProtocol {
		return manifest, fmt.Errorf("text findings require the v2 protocol")
	}
	input, err := os.Open(r.Input.Path)
	if err != nil {
		return manifest, err
	}
	defer input.Close()
	info, err := input.Stat()
	if err != nil {
		return manifest, err
	}
	format, err := audio.ReadWAVFormat(input, info.Size())
	if err != nil {
		return manifest, err
	}
	output, err := os.OpenFile(filepath.Join(stage, "findings.json"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return manifest, err
	}
	defer output.Close()
	bounded := &boundedTextWriter{writer: output, remaining: r.Limits.OutputBytes}
	languages := ""
	if r.Task == "translation" {
		languages = fmt.Sprintf(",\"language\":%q,\"target_language\":%q", r.Language, r.TargetLanguage)
	}
	if _, err = fmt.Fprintf(bounded, "{\"schema\":%q,\"task\":%q%s,\"windows\":[", FindingsSchema, r.Task, languages); err != nil {
		return manifest, err
	}
	windows := textWindows(r.Input)
	for index, window := range windows {
		if err = ctx.Err(); err != nil {
			return manifest, err
		}
		data, err := sourceWindowWAV(input, format.DataOffset, window.StartSample-r.Input.SourceOriginSamples, window.EndSample-window.StartSample)
		if err != nil {
			return manifest, fmt.Errorf("audio window %d: %w", index, err)
		}
		text, usage, err := s.Analyze(ctx, data, r.textTask())
		if err != nil {
			return manifest, fmt.Errorf("audio window %d: %w", index, err)
		}
		if strings.TrimSpace(text) == "" || !utf8.ValidString(text) || len(text) > 64<<10 {
			return manifest, fmt.Errorf("audio window %d returned empty, invalid or oversized text", index)
		}
		row, err := json.Marshal(TextWindow{StartSample: window.StartSample, EndSample: window.EndSample, InputSHA256: hashBytes(data), Text: text, Usage: usage})
		if err != nil {
			return manifest, err
		}
		if index > 0 {
			if _, err = bounded.Write([]byte(",")); err != nil {
				return manifest, err
			}
		}
		if _, err = bounded.Write(row); err != nil {
			return manifest, err
		}
	}
	if _, err = bounded.Write([]byte("]}\n")); err != nil {
		return manifest, err
	}
	if err = output.Close(); err != nil {
		return manifest, err
	}
	hash, err := fileHash(ctx, filepath.Join(stage, "findings.json"))
	if err != nil {
		return manifest, err
	}
	manifest = Manifest{Protocol: r.Protocol, JobID: r.JobID, Task: r.Task, Source: r.Source, Input: r.Input, Runtime: r.Runtime, RuntimeVersion: p.RuntimeVersion, ModelDigest: p.ModelDigest, ProjectorDigest: p.ProjectorDigest, RuntimeDigest: p.RuntimeDigest, Transport: "audio_to_text", Language: r.Language, TargetLanguage: r.TargetLanguage, DurationPolicy: r.DurationPolicy, SourceWindows: windows, Output: Output{Kind: "json", Path: "findings.json", SHA256: hash, Bytes: bounded.written}, InstructionSHA256: hashBytes([]byte(r.Instruction)), PromptSHA256: hashBytes([]byte(r.textTask().prompt())), ChunkPolicy: ChunkPolicy, TextSampling: &TextSampling{Seed: 42, Temperature: 0, MaxTokens: MaxTextTokens, ContextTokens: ManagedContextTokens}}
	return manifest, nil
}

// Preserve source PCM exactly; only the bounded window's container is rebuilt.
func sourceWindowWAV(input io.ReaderAt, dataOffset, start, count int64) ([]byte, error) {
	if start < 0 || count <= 0 || count > WindowSamples {
		return nil, fmt.Errorf("invalid bounded audio window")
	}
	wav := make([]byte, 44+count*2)
	copy(wav, "RIFF")
	binary.LittleEndian.PutUint32(wav[4:], uint32(len(wav)-8))
	copy(wav[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(wav[16:], 16)
	binary.LittleEndian.PutUint16(wav[20:], 1)
	binary.LittleEndian.PutUint16(wav[22:], 1)
	binary.LittleEndian.PutUint32(wav[24:], 16000)
	binary.LittleEndian.PutUint32(wav[28:], 32000)
	binary.LittleEndian.PutUint16(wav[32:], 2)
	binary.LittleEndian.PutUint16(wav[34:], 16)
	copy(wav[36:], "data")
	binary.LittleEndian.PutUint32(wav[40:], uint32(count*2))
	if _, err := input.ReadAt(wav[44:], dataOffset+start*2); err != nil {
		return nil, err
	}
	return wav, nil
}

type boundedTextWriter struct {
	writer             io.Writer
	remaining, written int64
}

func (w *boundedTextWriter) Write(data []byte) (int, error) {
	if int64(len(data)) > w.remaining {
		return 0, fmt.Errorf("audio findings exceed output limit")
	}
	n, err := w.writer.Write(data)
	w.remaining -= int64(n)
	w.written += int64(n)
	if n < len(data) && err == nil {
		err = io.ErrShortWrite
	}
	return n, err
}
