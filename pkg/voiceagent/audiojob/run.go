package audiojob

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
)

// Synthesizer is supplied by the owning finite command using the shared speech
// adapter; the job itself never initializes assistant or audio device resources.
type Synthesizer interface {
	Synthesize(context.Context, string) (*audio.PCMStream, error)
}
type Provenance struct {
	RuntimeVersion, ModelDigest    string
	ProjectorDigest, RuntimeDigest string
	Sampling                       *Sampling
}
type Event struct {
	Protocol     string `json:"protocol"`
	JobID        string `json:"job_id"`
	Seq          int    `json:"seq"`
	Type         string `json:"type"`
	Stage        string `json:"stage,omitempty"`
	ManifestPath string `json:"manifest_path,omitempty"`
	Message      string `json:"message,omitempty"`
}

// Preflight checks source bytes and output ownership before a command loads a
// model. Run repeats it at the artifact transaction boundary.
func (r Request) Preflight(ctx context.Context, c Config) error {
	if err := r.Validate(c); err != nil {
		return err
	}
	if err := checkInput(ctx, r.Input); err != nil {
		return err
	}
	if err := safeDirectory(c.StorageRoot, r.OutputDir); err != nil {
		return err
	}
	if _, err := os.Lstat(r.OutputDir); !os.IsNotExist(err) {
		return fmt.Errorf("output directory already exists or is inaccessible")
	}
	return nil
}

// Run validates the input by streaming, drains all producer frames, and publishes
// only complete artifacts. emit must backpressure rather than drop events.
func Run(ctx context.Context, c Config, r Request, s Synthesizer, p Provenance, emit func(Event) error) (manifest Manifest, err error) {
	callerContext := ctx
	seq := 0
	send := func(kind, stage, path, message string) error {
		e := Event{Protocol: Protocol, JobID: r.JobID, Seq: seq, Type: kind, Stage: stage, ManifestPath: path, Message: message}
		if e := emit(e); e != nil {
			return e
		}
		seq++
		return nil
	}
	defer func() {
		if err != nil {
			kind := "failed"
			if callerContext.Err() == context.Canceled {
				kind = "cancelled"
			}
			_ = send(kind, "", "", err.Error())
		}
	}()
	if err = r.Validate(c); err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(r.Limits.TimeoutSeconds)*time.Second)
	defer cancel()
	if err = r.Preflight(ctx, c); err != nil {
		return
	}
	if p.RuntimeVersion == "" || !HashValid(p.ModelDigest) {
		err = fmt.Errorf("missing verified runtime/model provenance")
		return
	}
	if c.Managed != nil && (p.RuntimeVersion != c.Managed.Version || p.ModelDigest != c.Managed.Model.SHA256 || p.ProjectorDigest != c.Managed.Projector.SHA256 || p.RuntimeDigest != c.Managed.Executable.SHA256 || p.Sampling == nil) {
		err = fmt.Errorf("managed runtime provenance does not match the selected pack")
		return
	}
	parent := filepath.Dir(r.OutputDir)
	lock, e := os.OpenFile(r.OutputDir+".lock", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if e != nil {
		err = fmt.Errorf("job output locked: %w", e)
		return
	}
	_ = lock.Close()
	defer os.Remove(r.OutputDir + ".lock")
	if _, e := os.Lstat(r.OutputDir); !os.IsNotExist(e) {
		err = fmt.Errorf("output directory already exists or is inaccessible")
		return
	}
	stage, e := os.MkdirTemp(parent, ".audio-job-")
	if e != nil {
		err = e
		return
	}
	defer os.RemoveAll(stage)
	if err = send("accepted", "", "", ""); err != nil {
		return
	}
	if err = send("progress", "synthesis", "", ""); err != nil {
		return
	}
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
	manifest = Manifest{Protocol: Protocol, JobID: r.JobID, Task: r.Task, Source: r.Source, Input: r.Input, Runtime: r.Runtime, RuntimeVersion: p.RuntimeVersion, ModelDigest: p.ModelDigest, Transport: "text_then_speech", ScriptSHA256: hex.EncodeToString(scriptHash[:]), Language: r.Language, DurationPolicy: r.DurationPolicy, SourceWindows: []Window{{StartSample: r.Input.SourceOriginSamples, EndSample: r.Input.SourceOriginSamples + r.Input.SampleCount}}, Output: Output{Path: "audio.wav", SHA256: hash, SampleRate: 24000, Channels: 1, SampleCount: samples}}
	manifest.ProjectorDigest, manifest.RuntimeDigest, manifest.Sampling = p.ProjectorDigest, p.RuntimeDigest, p.Sampling
	data, e := json.MarshalIndent(manifest, "", "  ")
	if e != nil {
		err = e
		return
	}
	if err = os.WriteFile(filepath.Join(stage, "manifest.json"), data, 0600); err != nil {
		return
	}
	for _, name := range []string{"audio.wav", "manifest.json"} {
		f, e := os.OpenFile(filepath.Join(stage, name), os.O_RDWR, 0)
		if e != nil {
			err = e
			return
		}
		e = f.Sync()
		_ = f.Close()
		if e != nil {
			err = e
			return
		}
	}
	if err = ctx.Err(); err != nil {
		return
	}
	stageDir, e := os.Open(stage)
	if e != nil {
		err = e
		return
	}
	e = stageDir.Sync()
	_ = stageDir.Close()
	if e != nil {
		err = e
		return
	}
	if err = ctx.Err(); err != nil {
		return
	}
	if err = os.Rename(stage, r.OutputDir); err != nil {
		return
	}
	// A broken event pipe cannot authorize an asset. Remove the publication so a
	// failed worker never leaves something a client might mistake for complete.
	defer func() {
		if err != nil {
			_ = os.RemoveAll(r.OutputDir)
		}
	}()
	dir, e := os.Open(parent)
	if e != nil {
		err = e
		return
	}
	e = dir.Sync()
	_ = dir.Close()
	if e != nil {
		err = e
		return
	}
	if err = ctx.Err(); err != nil {
		return
	}
	if err = send("artifact", "", filepath.Join(r.OutputDir, "manifest.json"), ""); err != nil {
		return
	}
	if err = ctx.Err(); err != nil {
		return
	}
	err = send("completed", "", "", "")
	return
}
func safeDirectory(root, output string) error {
	// Require every existing component to be a real directory. The caller owns
	// this root exclusively; other processes must not mutate it during the job.
	parent := filepath.Dir(output)
	for path := parent; ; path = filepath.Dir(path) {
		info, e := os.Lstat(path)
		if e != nil {
			return fmt.Errorf("output parent: %w", e)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("output parent contains symlink or non-directory")
		}
		if path == filepath.Clean(root) {
			break
		}
	}
	rel, e := filepath.Rel(root, parent)
	if e != nil || rel == ".." {
		return fmt.Errorf("output parent outside storage root")
	}
	return nil
}
func fileHash(ctx context.Context, path string) (string, error) {
	f, e := os.Open(path)
	if e != nil {
		return "", e
	}
	defer f.Close()
	h := sha256.New()
	buf := make([]byte, 64<<10)
	for {
		if e = ctx.Err(); e != nil {
			return "", e
		}
		n, re := f.Read(buf)
		if n > 0 {
			_, _ = h.Write(buf[:n])
		}
		if re == io.EOF {
			break
		}
		if re != nil {
			return "", re
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
func checkInput(ctx context.Context, in Input) error {
	info, e := os.Lstat(in.Path)
	if e != nil {
		return e
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("input must be a regular immutable WAV")
	}
	f, e := os.Open(in.Path)
	if e != nil {
		return e
	}
	defer f.Close()
	format, e := audio.ReadWAVFormat(f, info.Size())
	if e != nil {
		return e
	}
	if format.SampleRate != in.SampleRate || format.Channels != in.Channels || format.BitsPerSample != 16 || format.Frames() != in.SampleCount {
		return fmt.Errorf("input WAV metadata mismatch")
	}
	hash, e := fileHash(ctx, in.Path)
	if e != nil {
		return e
	}
	if hash != in.SHA256 {
		return fmt.Errorf("input WAV hash mismatch")
	}
	return nil
}
