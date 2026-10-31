package audiojob

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
)

func renderNarration(ctx context.Context, r Request, s Synthesizer, p Provenance, stage string) (manifest Manifest, err error) {
	if r.Protocol != Protocol {
		return manifest, fmt.Errorf("narration requires the v1 protocol")
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	writer, e := audio.NewWAVWriter(filepath.Join(stage, "audio.wav"), 24000)
	if e != nil {
		err = e
		return
	}
	stream, e := s.Synthesize(ctx, r.Script)
	if e != nil {
		_ = writer.Close()
		err = e
		return
	}
	var samples int64
	for frame := range stream.Frames() {
		if err == nil {
			if ctx.Err() != nil {
				err = ctx.Err()
			} else if samples+int64(len(frame)) > (r.Limits.OutputBytes-44)/2 {
				err = fmt.Errorf("generated audio exceeds output limit")
				cancel()
			} else if e = writer.Write(frame); e != nil {
				err = e
				cancel()
			} else {
				samples += int64(len(frame))
			}
		}
	}
	if e = stream.Err(); err == nil && e != nil {
		err = e
	}
	if e = writer.Close(); err == nil && e != nil {
		err = e
	}
	if err != nil {
		return
	}
	if ctx.Err() != nil {
		err = ctx.Err()
		return
	}
	if samples == 0 {
		err = fmt.Errorf("empty generated audio")
		return
	}
	hash, e := fileHash(ctx, filepath.Join(stage, "audio.wav"))
	if e != nil {
		err = e
		return
	}
	scriptHash := sha256.Sum256([]byte(r.Script))
	manifest = Manifest{Protocol: r.Protocol, JobID: r.JobID, Task: r.Task, Source: r.Source, Input: r.Input, Runtime: r.Runtime, RuntimeVersion: p.RuntimeVersion, ModelDigest: p.ModelDigest, Transport: "text_then_speech", ScriptSHA256: hex.EncodeToString(scriptHash[:]), Language: r.Language, DurationPolicy: r.DurationPolicy, SourceWindows: []Window{{StartSample: r.Input.SourceOriginSamples, EndSample: r.Input.SourceOriginSamples + r.Input.SampleCount}}, Output: Output{Path: "audio.wav", SHA256: hash, SampleRate: 24000, Channels: 1, SampleCount: samples}}
	manifest.ProjectorDigest, manifest.RuntimeDigest, manifest.Sampling = p.ProjectorDigest, p.RuntimeDigest, p.Sampling
	return
}
