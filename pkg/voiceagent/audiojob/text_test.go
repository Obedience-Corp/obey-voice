package audiojob

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
)

type analyzeFunc func(context.Context, []byte, TextTask) (string, TextUsage, error)

func (f analyzeFunc) Analyze(ctx context.Context, wav []byte, task TextTask) (string, TextUsage, error) {
	return f(ctx, wav, task)
}
func textFixture(t *testing.T) (Config, Request, Provenance, []byte) {
	t.Helper()
	c, r := managedFixture(t, "healthy")
	r.Protocol = TextProtocol
	r.Task = "transcription"
	r.Script = ""
	samples := make([]float32, 61*16000)
	for i := range samples {
		samples[i] = float32(i%199-99) / 100
	}
	if err := audio.WriteWAVFloat32(r.Input.Path, 16000, samples); err != nil {
		t.Fatal(err)
	}
	r.Input.SampleCount = int64(len(samples))
	r.Input.SHA256, _ = fileHash(t.Context(), r.Input.Path)
	data, err := os.ReadFile(r.Input.Path)
	if err != nil {
		t.Fatal(err)
	}
	p := Provenance{RuntimeVersion: ManagedVersion, ModelDigest: c.Managed.Model.SHA256, ProjectorDigest: c.Managed.Projector.SHA256, RuntimeDigest: c.Managed.Executable.SHA256, Sampling: &Sampling{Seed: 42, MaxAudioFrames: 8192, RepeatPenalty: 1.05}}
	return c, r, p, data
}

func TestAudioTextWindowsPreserveAllPCMAndSourceOffsets(t *testing.T) {
	c, r, p, input := textFixture(t)
	windows := []Window{{32000, 512000}, {480000, 960000}, {928000, 1008000}}
	calls := 0
	analyzer := analyzeFunc(func(ctx context.Context, wav []byte, task TextTask) (string, TextUsage, error) {
		window := windows[calls]
		calls++
		format, err := audio.ReadWAVFormat(bytes.NewReader(wav), int64(len(wav)))
		if err != nil {
			t.Fatal(err)
		}
		start, end := window.StartSample-r.Input.SourceOriginSamples, window.EndSample-r.Input.SourceOriginSamples
		if format.Frames() != end-start || !bytes.Equal(wav[format.DataOffset:], input[44+start*2:44+end*2]) {
			t.Fatal("window changed or lost source samples")
		}
		if task.Task != "transcription" || task.Instruction != "" || task.Language != "en" || task.TargetLanguage != "" {
			t.Fatal("task changed")
		}
		return fmt.Sprintf("window %d, including overlap", calls), TextUsage{PromptTokens: 400, CompletionTokens: 12}, nil
	})
	var events []Event
	m, err := RunText(t.Context(), c, r, analyzer, p, func(e Event) error { events = append(events, e); return nil })
	if err != nil {
		t.Fatal(err)
	}
	if calls != 3 || m.Transport != "audio_to_text" || m.Output.Kind != "json" || m.Output.SampleCount != 0 || m.ChunkPolicy != ChunkPolicy || m.Source != r.Source || m.PromptSHA256 != hashBytes([]byte(r.textTask().prompt())) {
		t.Fatal("wrong findings provenance", m)
	}
	data, err := os.ReadFile(filepath.Join(r.OutputDir, m.Output.Path))
	if err != nil {
		t.Fatal(err)
	}
	if int64(len(data)) != m.Output.Bytes || hashBytes(data) != m.Output.SHA256 {
		t.Fatal("wrong output hash/size")
	}
	var findings struct {
		Schema  string       `json:"schema"`
		Task    string       `json:"task"`
		Windows []TextWindow `json:"windows"`
	}
	if err = json.Unmarshal(data, &findings); err != nil {
		t.Fatal(err)
	}
	if findings.Schema != FindingsSchema || findings.Task != r.Task || len(findings.Windows) != len(windows) {
		t.Fatal(findings)
	}
	for i, w := range findings.Windows {
		if w.StartSample != windows[i].StartSample || w.EndSample != windows[i].EndSample || !HashValid(w.InputSHA256) || m.SourceWindows[i] != windows[i] {
			t.Fatal(w)
		}
	}
	for i, e := range events {
		if e.Seq != i || e.Protocol != TextProtocol {
			t.Fatal(e)
		}
	}
	if events[len(events)-1].Type != "completed" {
		t.Fatal(events)
	}
	if _, err = os.Stat(filepath.Join(r.OutputDir, "audio.wav")); !os.IsNotExist(err) {
		t.Fatal("text job published audio")
	}
}

func TestAudioTextFailureNeverPublishesPartialWindows(t *testing.T) {
	for _, mode := range []string{"provider", "cancel", "source-changed", "limit", "empty"} {
		t.Run(mode, func(t *testing.T) {
			c, r, p, _ := textFixture(t)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if mode == "limit" {
				r.Limits.OutputBytes = 44
			}
			calls := 0
			analyzer := analyzeFunc(func(context.Context, []byte, TextTask) (string, TextUsage, error) {
				calls++
				if mode == "source-changed" && calls == 1 {
					f, err := os.OpenFile(r.Input.Path, os.O_WRONLY, 0)
					if err != nil {
						t.Fatal(err)
					}
					_, err = f.WriteAt([]byte{1, 2}, 44)
					_ = f.Close()
					if err != nil {
						t.Fatal(err)
					}
				}
				if calls == 2 {
					switch mode {
					case "provider":
						return "", TextUsage{}, errors.New("inference failed")
					case "cancel":
						cancel()
						return "", TextUsage{}, ctx.Err()
					case "empty":
						return " ", TextUsage{}, nil
					}
				}
				return "A draft.", TextUsage{PromptTokens: 400, CompletionTokens: 3}, nil
			})
			var events []Event
			_, err := RunText(ctx, c, r, analyzer, p, func(e Event) error { events = append(events, e); return nil })
			if err == nil {
				t.Fatal("incomplete findings accepted")
			}
			if _, err = os.Stat(r.OutputDir); !os.IsNotExist(err) {
				t.Fatal("partial output survived")
			}
			matches, _ := filepath.Glob(filepath.Join(c.StorageRoot, ".audio-job-*"))
			if len(matches) != 0 {
				t.Fatal("stage leaked")
			}
			if _, err = os.Stat(r.OutputDir + ".lock"); !os.IsNotExist(err) {
				t.Fatal("lock leaked")
			}
			terminal := "failed"
			if mode == "cancel" {
				terminal = "cancelled"
			}
			if len(events) == 0 || events[len(events)-1].Type != terminal {
				t.Fatal(events)
			}
		})
	}
}

func TestAudioTextExplicitProtocolAndTaskBoundaries(t *testing.T) {
	c, r, _, _ := textFixture(t)
	if err := r.Validate(c); err != nil {
		t.Fatal(err)
	}
	r.Task = "understanding"
	r.Instruction = "What is being designed?"
	if err := r.Validate(c); err != nil {
		t.Fatal(err)
	}
	cases := []func(*Config, *Request){
		func(_ *Config, r *Request) { r.Protocol = Protocol },
		func(_ *Config, r *Request) { r.Task = "narration" },
		func(_ *Config, r *Request) { r.Script = "hidden narration" },
		func(_ *Config, r *Request) { r.Instruction = "" },
		func(_ *Config, r *Request) { r.Task = "transcription" },
		func(c *Config, r *Request) {
			c.Managed = nil
			c.Runtime.Provider = "ollama"
			c.Runtime.Endpoint = "http://127.0.0.1:11434"
			r.Runtime = c.Runtime
		},
	}
	for i, change := range cases {
		candidate, request := c, r
		change(&candidate, &request)
		if err := request.Validate(candidate); err == nil {
			t.Fatalf("invalid case %d accepted", i)
		}
	}
}
