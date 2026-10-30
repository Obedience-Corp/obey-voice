package audiojob

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
)

type synth struct {
	calls int
	fail  bool
}

func (s *synth) Synthesize(ctx context.Context, _ string) (*audio.PCMStream, error) {
	s.calls++
	p := audio.NewPCMStream(ctx)
	go func() {
		_ = p.SetSampleRate(24000)
		_ = p.Write(make([]float32, 240))
		if s.fail {
			p.CloseWithError(fmt.Errorf("provider failed"))
		} else {
			p.Close()
		}
	}()
	return p, nil
}
func fixture(t *testing.T) (Config, Request) {
	t.Helper()
	root := t.TempDir()
	input := filepath.Join(root, "input.wav")
	if e := audio.WriteWAVFloat32(input, 16000, make([]float32, 160)); e != nil {
		t.Fatal(e)
	}
	hash, e := fileHash(context.Background(), input)
	if e != nil {
		t.Fatal(e)
	}
	runtime := Runtime{Provider: "ollama", Endpoint: "http://127.0.0.1:11434", Model: "lfm3-audio:2b", Voice: "female"}
	c := Config{Schema: ConfigSchema, Runtime: runtime, StorageRoot: root}
	r := Request{Protocol: Protocol, JobID: "job-1", Task: "narration", Source: Source{MediaSHA256: strings.Repeat("1", 64), ClipID: "clip", RangeID: "range", ProjectRevision: "4"}, Input: Input{Path: input, SHA256: hash, SampleCount: 160, SampleRate: 16000, Channels: 1, ChannelRole: "microphone", SourceOriginSamples: 32000}, Runtime: runtime, Script: "Approved words.", Language: "en", DurationPolicy: "preserve_picture", OutputDir: filepath.Join(root, "job-1"), Limits: Limits{TimeoutSeconds: 5, OutputBytes: 1 << 20}}
	return c, r
}
func TestPublishExactSamplesAndIdentity(t *testing.T) {
	c, r := fixture(t)
	s := &synth{}
	var events []Event
	m, e := Run(context.Background(), c, r, s, Provenance{"0.40.12", strings.Repeat("a", 64)}, func(e Event) error { events = append(events, e); return nil })
	if e != nil {
		t.Fatal(e)
	}
	if m.Output.SampleCount != 240 || m.Transport != "text_then_speech" || m.Source != r.Source || m.SourceWindows[0] != (Window{32000, 32160}) {
		t.Fatalf("wrong manifest %+v", m)
	}
	want := []string{"accepted", "progress", "artifact", "completed"}
	for i, e := range events {
		if e.Type != want[i] || e.Seq != i || e.JobID != r.JobID {
			t.Fatalf("wrong event %+v", e)
		}
	}
	if _, e := os.Stat(filepath.Join(r.OutputDir, "audio.wav")); e != nil {
		t.Fatal(e)
	}
	_, e = Run(context.Background(), c, r, s, Provenance{"version", strings.Repeat("a", 64)}, func(Event) error { return nil })
	if e == nil || s.calls != 1 {
		t.Fatal("existing job was overwritten")
	}
}
func TestFailureAndCancellationDoNotPublish(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		t.Run(map[bool]string{false: "provider", true: "cancelled"}[cancelled], func(t *testing.T) {
			c, r := fixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if cancelled {
				cancel()
			}
			s := &synth{fail: true}
			_, e := Run(ctx, c, r, s, Provenance{"version", strings.Repeat("a", 64)}, func(Event) error { return nil })
			if e == nil {
				t.Fatal("accepted failed stream")
			}
			if _, e = os.Stat(r.OutputDir); !os.IsNotExist(e) {
				t.Fatal("partial job published")
			}
			matches, _ := filepath.Glob(filepath.Join(c.StorageRoot, ".audio-job-*"))
			if len(matches) != 0 {
				t.Fatal("staging leaked")
			}
		})
	}
}
func TestRejectBeforeInference(t *testing.T) {
	cases := map[string]func(*Config, *Request){"remote": func(c *Config, r *Request) { c.Runtime.Endpoint = "http://example.com"; r.Runtime = c.Runtime }, "override": func(c *Config, r *Request) { r.Runtime.Model = "other" }, "conversion": func(c *Config, r *Request) { r.Task = "voice_conversion" }, "hash": func(c *Config, r *Request) { r.Input.SHA256 = strings.Repeat("0", 64) }, "count": func(c *Config, r *Request) { r.Input.SampleCount++ }, "escape": func(c *Config, r *Request) { r.OutputDir = filepath.Join(c.StorageRoot, "..", "escaped") }, "symlink": func(c *Config, r *Request) {
		link := filepath.Join(c.StorageRoot, "link")
		if e := os.Symlink(t.TempDir(), link); e != nil {
			t.Fatal(e)
		}
		r.OutputDir = filepath.Join(link, "job")
	}}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			c, r := fixture(t)
			change(&c, &r)
			s := &synth{}
			_, e := Run(context.Background(), c, r, s, Provenance{"version", strings.Repeat("a", 64)}, func(Event) error { return nil })
			if e == nil || s.calls != 0 {
				t.Fatalf("not rejected before inference: %v calls=%d", e, s.calls)
			}
		})
	}
}

func TestTerminalClassifiesOriginalCause(t *testing.T) {
	for _, kind := range []string{"provider", "input", "limit", "caller", "deadline", "overflow"} {
		t.Run(kind, func(t *testing.T) {
			c, r := fixture(t)
			s := &synth{}
			ctx := context.Background()
			var cancel context.CancelFunc
			switch kind {
			case "provider":
				s.fail = true
			case "input":
				r.Input.SHA256 = strings.Repeat("0", 64)
			case "limit":
				r.Limits.OutputBytes = 44
			case "overflow":
				r.Input.SourceOriginSamples = math.MaxInt64
			case "caller":
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			case "deadline":
				ctx, cancel = context.WithDeadline(ctx, time.Now().Add(-time.Second))
				defer cancel()
			}
			var events []Event
			_, err := Run(ctx, c, r, s, Provenance{"version", strings.Repeat("a", 64)}, func(e Event) error { events = append(events, e); return nil })
			if err == nil || len(events) == 0 {
				t.Fatal("expected terminal failure")
			}
			last := events[len(events)-1]
			want := "failed"
			if kind == "caller" {
				want = "cancelled"
			}
			if last.Type != want {
				t.Fatalf("%s: %+v", kind, last)
			}
			if kind == "deadline" && !strings.Contains(last.Message, "deadline exceeded") {
				t.Fatal("deadline cause lost", last)
			}
		})
	}
}

func TestCancelOnArtifactNeverCompletesOrLeavesAsset(t *testing.T) {
	c, r := fixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var events []Event
	_, e := Run(ctx, c, r, &synth{}, Provenance{"version", strings.Repeat("a", 64)}, func(e Event) error {
		events = append(events, e)
		if e.Type == "artifact" {
			cancel()
		}
		return nil
	})
	if e == nil {
		t.Fatal("late cancellation accepted")
	}
	for _, e := range events {
		if e.Type == "completed" {
			t.Fatal("completed after cancellation")
		}
	}
	if events[len(events)-1].Type != "cancelled" {
		t.Fatal(events)
	}
	if _, e := os.Stat(r.OutputDir); !os.IsNotExist(e) {
		t.Fatal("cancelled asset remains")
	}
}
