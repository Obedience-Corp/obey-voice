//go:build !integration

package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/transcription"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/transcription/protocol"
)

var updateTranscribeGolden = flag.Bool("update-transcribe-golden", false,
	"rewrite cmd/obey-voice/cmd/testdata/transcribe/golden from the current behaviour")

const transcribeFixtureDir = "testdata/transcribe"

// transcribeFixture ties a request document to the deps that produce its
// scenario and to the exit status the contract assigns it.
type transcribeFixture struct {
	name string
	exit int
	mut  func(d *transcribeDeps)
}

func transcribeFixtures() []transcribeFixture {
	segment := func(id string, startUS, endUS int64, text string) transcription.Segment {
		return transcription.Segment{ID: id, StartUS: startUS, EndUS: endUS, Text: text}
	}
	return []transcribeFixture{
		{name: "bad_version", exit: protocol.ExitInvalid},
		{name: "unsupported_timing", exit: protocol.ExitInvalid},
		{name: "unsupported_language", exit: protocol.ExitInvalid},
		{
			name: "missing_model",
			exit: protocol.ExitUnavailable,
			mut: func(d *transcribeDeps) {
				d.manifest = func(*config.Config) (config.AssetManifest, error) {
					return config.AssetManifest{Schema: "obey-voice.assets.v1", Assets: []config.Asset{config.VADAsset()}}, nil
				}
			},
		},
		{
			name: "wrong_rate",
			exit: protocol.ExitInvalid,
			mut: func(d *transcribeDeps) {
				d.openSource = func(string) (transcribeSource, error) {
					return &fakeTranscribeSource{format: audio.WAVFormat{SampleRate: 48000, Channels: 1, BitsPerSample: 16}}, nil
				}
			},
		},
		{
			name: "stereo",
			exit: protocol.ExitInvalid,
			mut: func(d *transcribeDeps) {
				d.openSource = func(string) (transcribeSource, error) {
					return &fakeTranscribeSource{format: audio.WAVFormat{SampleRate: 16000, Channels: 2, BitsPerSample: 16}}, nil
				}
			},
		},
		{
			name: "truncated",
			exit: protocol.ExitInvalid,
			mut: func(d *transcribeDeps) {
				d.openSource = func(string) (transcribeSource, error) {
					return nil, errors.New("audio: wav chunk \"data\" declares 3200000 bytes but only 1600 remain")
				}
			},
		},
		{
			name: "eof_speech",
			exit: protocol.ExitOK,
			mut: func(d *transcribeDeps) {
				d.run = func(ctx context.Context, _ transcription.Request, deps transcription.Deps) (transcription.Summary, error) {
					if err := deps.Sink.Emit(ctx, acceptedFrom(deps.Info)); err != nil {
						return transcription.Summary{}, err
					}
					if err := deps.Sink.Emit(ctx, segment("s1", 800000, 2400000, "Here is the recording.")); err != nil {
						return transcription.Summary{}, err
					}
					if err := deps.Sink.Emit(ctx, transcription.Progress{ConsumedUS: 5_000_000, DurationUS: 6_300_000}); err != nil {
						return transcription.Summary{}, err
					}
					if err := deps.Sink.Emit(ctx, segment("s2", 5_100_000, 6_300_000, "Trailing words at the very end.")); err != nil {
						return transcription.Summary{}, err
					}
					return transcription.Summary{DurationUS: 6_300_000, SegmentCount: 2, Outcome: transcription.OutcomeSpeech}, nil
				}
			},
		},
		{
			name: "no_speech",
			exit: protocol.ExitOK,
			mut: func(d *transcribeDeps) {
				d.run = func(ctx context.Context, _ transcription.Request, deps transcription.Deps) (transcription.Summary, error) {
					if err := deps.Sink.Emit(ctx, acceptedFrom(deps.Info)); err != nil {
						return transcription.Summary{}, err
					}
					return transcription.Summary{DurationUS: 30_000_000, Outcome: transcription.OutcomeNoSpeech}, nil
				}
			},
		},
		{
			name: "cancelled",
			exit: protocol.ExitCancelled,
			mut: func(d *transcribeDeps) {
				ctx, cancel := context.WithCancel(context.Background())
				d.newContext = func() (context.Context, context.CancelFunc) { return ctx, cancel }
				d.run = func(ctx context.Context, _ transcription.Request, deps transcription.Deps) (transcription.Summary, error) {
					if err := deps.Sink.Emit(ctx, acceptedFrom(deps.Info)); err != nil {
						return transcription.Summary{}, err
					}
					if err := deps.Sink.Emit(ctx, segment("s1", 0, 1_500_000, "Only this much was decoded.")); err != nil {
						return transcription.Summary{}, err
					}
					cancel()
					return transcription.Summary{}, ctx.Err()
				}
			},
		},
	}
}

// materializeRequest writes the fixture's request with a real input path and
// returns the request path, the input path it substituted, and the job ID.
func materializeRequest(t *testing.T, name string) (reqPath, inputPath, jobID string) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(transcribeFixtureDir, "requests", name+".json"))
	if err != nil {
		t.Fatalf("read fixture request: %v", err)
	}
	dir := t.TempDir()
	inputPath = filepath.Join(dir, "audio.wav")
	doc := strings.ReplaceAll(string(raw), "{{INPUT}}", inputPath)

	var head struct {
		JobID string `json:"job_id"`
	}
	if err := json.Unmarshal([]byte(doc), &head); err != nil {
		t.Fatalf("fixture request is not JSON: %v", err)
	}
	reqPath = filepath.Join(dir, "request.json")
	if err := os.WriteFile(reqPath, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	return reqPath, inputPath, head.JobID
}

// runTranscribeFixture runs one fixture and returns its stream with every
// machine-specific path replaced by the placeholder the golden carries.
func runTranscribeFixture(t *testing.T, fx transcribeFixture) (stream, jobID string, exit int) {
	t.Helper()
	var counts transcribeDepsCounts
	deps := fakeTranscribeDeps(t, &counts)
	if fx.mut != nil {
		fx.mut(&deps)
	}
	cfg, err := deps.loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	reqPath, inputPath, jobID := materializeRequest(t, fx.name)
	res := runTranscribeCommand(t, deps, "--request", reqPath, "--jsonl")

	stream = strings.ReplaceAll(res.stdout, inputPath, "{{INPUT}}")
	stream = strings.ReplaceAll(stream, deps.modelsDir(cfg), "{{MODELS_DIR}}")
	return stream, jobID, res.exit
}

func TestTranscribeConformanceFixtures(t *testing.T) {
	for _, fx := range transcribeFixtures() {
		t.Run(fx.name, func(t *testing.T) {
			got, jobID, exit := runTranscribeFixture(t, fx)
			if exit != fx.exit {
				t.Errorf("exit = %d, want %d\n%s", exit, fx.exit, got)
			}

			// The generated stream is checked before any golden is written, so
			// an invalid stream cannot be blessed into the repository.
			decodeStream(t, got, jobID)

			goldenPath := filepath.Join(transcribeFixtureDir, "golden", fx.name+".jsonl")
			if *updateTranscribeGolden {
				if err := os.WriteFile(goldenPath, []byte(got), 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(goldenPath)
			if err != nil {
				t.Fatalf("read golden (rerun with -update-transcribe-golden to create it): %v", err)
			}
			if got != string(want) {
				t.Errorf("stream does not match %s\n got: %s\nwant: %s", goldenPath, got, want)
			}
		})
	}
}

// TestTranscribeRepeatedJobIDProducesIndependentStreams documents the rule a
// caller owns: the command does not remember job IDs, so submitting the same
// one twice gives two complete, identical streams. Deduplication belongs to
// whatever assigns the IDs.
func TestTranscribeRepeatedJobIDProducesIndependentStreams(t *testing.T) {
	fx := fixtureNamed(t, "no_speech")
	first, _, firstExit := runTranscribeFixture(t, fx)
	second, _, secondExit := runTranscribeFixture(t, fx)

	if firstExit != protocol.ExitOK || secondExit != protocol.ExitOK {
		t.Fatalf("exits = %d, %d", firstExit, secondExit)
	}
	if first != second {
		t.Fatalf("the same job ID gave different streams:\n%s\n%s", first, second)
	}
}

// TestTranscribeGoldenFilesAreAllUsed keeps the fixture directory honest.
func TestTranscribeGoldenFilesAreAllUsed(t *testing.T) {
	entries, err := os.ReadDir(filepath.Join(transcribeFixtureDir, "golden"))
	if err != nil {
		t.Fatal(err)
	}
	known := map[string]bool{}
	for _, fx := range transcribeFixtures() {
		known[fx.name+".jsonl"] = true
	}
	for _, e := range entries {
		if !known[e.Name()] {
			t.Errorf("%s has no fixture; delete it or add the scenario", e.Name())
		}
	}
	if len(entries) != len(known) {
		t.Errorf("%d golden files for %d fixtures", len(entries), len(known))
	}
}

func fixtureNamed(t *testing.T, name string) transcribeFixture {
	t.Helper()
	for _, fx := range transcribeFixtures() {
		if fx.name == name {
			return fx
		}
	}
	t.Fatalf("no fixture named %q", name)
	return transcribeFixture{}
}
