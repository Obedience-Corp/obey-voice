//go:build !integration

package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/stt"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/transcription"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/transcription/protocol"
)

func writeRequest(t *testing.T, doc string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "request.json")
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func validRequestDoc(t *testing.T, overrides map[string]any) string {
	t.Helper()
	req := map[string]any{
		"protocol": protocol.Protocol,
		"job_id":   "job-1",
		"input": map[string]any{
			"path":               filepath.Join(t.TempDir(), "audio.wav"),
			"format":             protocol.FormatWAVPCMS16LE,
			"sample_rate_hz":     16000,
			"channels":           1,
			"timeline_origin_us": 0,
		},
		"timing": protocol.TimingUtterance,
	}
	for k, v := range overrides {
		req[k] = v
	}
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

type transcribeResult struct {
	stdout string
	stderr string
	exit   int
}

func runTranscribeCommand(t *testing.T, deps transcribeDeps, args ...string) transcribeResult {
	t.Helper()
	cmd := newTranscribeCmd(deps)
	var out, errBuf bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errBuf)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return transcribeResult{stdout: out.String(), stderr: errBuf.String(), exit: ExitCode(err)}
}

// decodeStream applies the client rules to the command's stdout.
func decodeStream(t *testing.T, stdout, jobID string) []protocol.Event {
	t.Helper()
	dec := protocol.NewDecoder(strings.NewReader(stdout), jobID)
	var events []protocol.Event
	for {
		ev, err := dec.Next()
		if errors.Is(err, io.EOF) {
			return events
		}
		if err != nil {
			t.Fatalf("stdout is not a valid obey-transcript/v1 stream: %v\n%s", err, stdout)
		}
		events = append(events, ev)
	}
}

func TestTranscribeRequiresItsFlags(t *testing.T) {
	deps := defaultTranscribeDeps()
	cases := []struct {
		name string
		args []string
	}{
		{"no request", []string{"--jsonl"}},
		{"no jsonl", []string{"--request", writeRequest(t, validRequestDoc(t, nil))}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := runTranscribeCommand(t, deps, tc.args...)
			if res.exit != exitUsage {
				t.Errorf("exit = %d, want %d", res.exit, exitUsage)
			}
			if res.stdout != "" {
				t.Errorf("stdout carries %q; a usage error is not a stream", res.stdout)
			}
		})
	}
}

func TestTranscribeReportsAnInvalidRequestAsAFailedEvent(t *testing.T) {
	doc := `{"protocol":"obey-transcript/v2","job_id":"job-1","input":{"path":"/a.wav","format":"wav_pcm_s16le","sample_rate_hz":16000,"channels":1,"timeline_origin_us":0},"timing":"utterance"}`
	res := runTranscribeCommand(t, defaultTranscribeDeps(), "--request", writeRequest(t, doc), "--jsonl")

	if res.exit != protocol.ExitInvalid {
		t.Fatalf("exit = %d, want %d\nstderr: %s", res.exit, protocol.ExitInvalid, res.stderr)
	}
	events := decodeStream(t, res.stdout, "job-1")
	if len(events) != 1 {
		t.Fatalf("got %d events, want one failed event:\n%s", len(events), res.stdout)
	}
	failed, ok := events[0].(*protocol.Failed)
	if !ok || failed.Code != protocol.CodeUnsupportedProtocol {
		t.Fatalf("event = %+v, want %s", events[0], protocol.CodeUnsupportedProtocol)
	}
}

func TestTranscribeWritesNothingToStdoutWithoutAJobID(t *testing.T) {
	// Otherwise-valid JSON whose only defect is an empty or omitted job_id:
	// parsing succeeds, validation fails, and there is no stream to write.
	var omitted map[string]any
	if err := json.Unmarshal([]byte(validRequestDoc(t, nil)), &omitted); err != nil {
		t.Fatal(err)
	}
	delete(omitted, "job_id")
	omittedDoc, err := json.Marshal(omitted)
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		doc  string
	}{
		{"empty", validRequestDoc(t, map[string]any{"job_id": ""})},
		{"omitted", string(omittedDoc)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := runTranscribeCommand(t, defaultTranscribeDeps(), "--request", writeRequest(t, tc.doc), "--jsonl")

			if res.exit != protocol.ExitInvalid {
				t.Fatalf("exit = %d, want %d", res.exit, protocol.ExitInvalid)
			}
			if res.stdout != "" {
				t.Fatalf("stdout carries %q; a stream needs a job ID", res.stdout)
			}
			if res.stderr == "" {
				t.Error("nothing on stderr explains the failure")
			}
		})
	}
}

func TestTranscribeFailureOversizedInvalidRequestBecomesInternal(t *testing.T) {
	var out bytes.Buffer
	enc := protocol.NewEncoder(&out, "job-1")
	cause := protocol.Errorf(protocol.CodeInvalidRequest, false, "%s", strings.Repeat("x", protocol.MaxLineBytes))

	err := transcribeFailure(enc, cause)
	if got := ExitCode(err); got != protocol.ExitRuntime {
		t.Fatalf("exit = %d, want %d\nerror: %v", got, protocol.ExitRuntime, err)
	}
	if err == nil || err.Error() == "" {
		t.Fatal("human error is nil or empty")
	}

	events := decodeStream(t, out.String(), "job-1")
	if len(events) != 1 {
		t.Fatalf("got %d events, want one failed event:\n%s", len(events), out.String())
	}
	failed, ok := events[0].(*protocol.Failed)
	if !ok || failed.Code != protocol.CodeInternal {
		t.Fatalf("event = %+v, want %s", events[0], protocol.CodeInternal)
	}
}

// TestTranscribeIsRegistered keeps the command reachable from the root tree.
func TestTranscribeIsRegistered(t *testing.T) {
	for _, c := range rootCmd.Commands() {
		if c.Name() == "transcribe" {
			return
		}
	}
	t.Fatal("transcribe is not registered on the root command")
}

// fakeTranscribeSource is a source that opened successfully and reports a
// format. The runner is faked too, so it never reads a frame.
type fakeTranscribeSource struct {
	format audio.WAVFormat
}

func (f *fakeTranscribeSource) ReadFrame(context.Context) (audio.Frame, error) {
	return audio.Frame{}, audio.ErrSourceClosed
}
func (f *fakeTranscribeSource) Close() error            { return nil }
func (f *fakeTranscribeSource) Position() int64         { return 0 }
func (f *fakeTranscribeSource) TotalSamples() int64     { return 16000 }
func (f *fakeTranscribeSource) Format() audio.WAVFormat { return f.format }

type fakeDecoder struct{}

func (fakeDecoder) DecodeSegment(context.Context, []float32) (string, error) { return "", nil }

type fakeDetector struct{}

func (fakeDetector) Accept([]float32)                 {}
func (fakeDetector) Next() (transcription.Span, bool) { return transcription.Span{}, false }
func (fakeDetector) Flush()                           {}
func (fakeDetector) Fed() int64                       { return 0 }

// acceptedFrom builds the accepted event the real runner builds from the
// resolved decoder, so a fake runner reports the same thing the real one does.
func acceptedFrom(info stt.DecoderInfo) transcription.Accepted {
	return transcription.Accepted{
		Provider:      info.Provider,
		Mode:          info.Mode,
		ModelID:       info.ModelID,
		ModelRevision: info.ModelRevision,
		Languages:     info.Languages,
		Timing:        protocol.TimingUtterance,
	}
}

// transcribeDepsCounts records that the read-only surfaces stayed read-only.
type transcribeDepsCounts struct {
	decoders  int
	detectors int
	runs      int
}

// installedManifest returns a manifest whose every asset exists under
// modelsDir, so Status reports it installed with no real model on disk. The
// assets carry no digest or size on purpose: then existence is the only test
// Status applies.
func installedManifest(t *testing.T, modelsDir string) config.AssetManifest {
	t.Helper()
	for _, rel := range []string{"silero_vad.onnx", "parakeet/tokens.txt"} {
		path := filepath.Join(modelsDir, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("stand-in"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return config.AssetManifest{
		Schema: "obey-voice.assets.v1",
		Assets: []config.Asset{
			{
				ID: "vad.silero.v1", Provider: "sherpa", Kind: config.AssetKindVAD, Name: "silero_vad.onnx",
				Files: []config.AssetFile{{Path: "silero_vad.onnx", URL: "https://example.invalid/silero_vad.onnx"}},
			},
			{
				ID: "stt.parakeet.offline.tdt-0.6b-v2", Provider: "parakeet", Mode: "offline",
				Kind: config.AssetKindSTT, Name: "tdt-0.6b-v2", TargetDir: "parakeet",
				Archive:    &config.AssetArchive{URL: "https://example.invalid/p.tar.bz2"},
				CheckFiles: []string{"tokens.txt"},
			},
		},
	}
}

func fakeTranscribeDeps(t *testing.T, counts *transcribeDepsCounts) transcribeDeps {
	t.Helper()
	cfg := &config.Config{
		STTProvider:   "sherpa-parakeet",
		ParakeetModel: config.DefaultParakeetModel,
		ModelsDir:     t.TempDir(),
		VADPreRollMS:  300,
	}
	manifest := installedManifest(t, cfg.ModelsDir)
	return transcribeDeps{
		loadConfig: func() (*config.Config, error) { return cfg, nil },
		manifest:   func(*config.Config) (config.AssetManifest, error) { return manifest, nil },
		modelsDir:  config.ModelsDirFrom,
		openSource: func(string) (transcribeSource, error) {
			return &fakeTranscribeSource{format: audio.WAVFormat{SampleRate: 16000, Channels: 1, BitsPerSample: 16}}, nil
		},
		newDecoder: func(*config.Config) (stt.SegmentDecoder, stt.DecoderInfo, func(), error) {
			counts.decoders++
			return fakeDecoder{}, stt.DecoderInfo{
				Provider:      "parakeet",
				Mode:          "offline",
				ModelID:       "tdt-0.6b-v2",
				ModelRevision: "157c157b",
				Timing:        []string{protocol.TimingUtterance},
			}, func() {}, nil
		},
		newDetector: func(*config.Config) (transcription.SpanDetector, func(), error) {
			counts.detectors++
			return fakeDetector{}, func() {}, nil
		},
		run: func(ctx context.Context, _ transcription.Request, deps transcription.Deps) (transcription.Summary, error) {
			counts.runs++
			if err := deps.Sink.Emit(ctx, acceptedFrom(deps.Info)); err != nil {
				return transcription.Summary{}, err
			}
			if err := deps.Sink.Emit(ctx, transcription.Segment{ID: "s1", StartUS: 800000, EndUS: 2400000, Text: "Here is the recording."}); err != nil {
				return transcription.Summary{}, err
			}
			return transcription.Summary{DurationUS: 3200000, SegmentCount: 1, Outcome: transcription.OutcomeSpeech}, nil
		},
		newContext: func() (context.Context, context.CancelFunc) { return context.WithCancel(context.Background()) },
	}
}

func TestTranscribeEmitsAcceptedSegmentAndCompleted(t *testing.T) {
	var counts transcribeDepsCounts
	deps := fakeTranscribeDeps(t, &counts)
	res := runTranscribeCommand(t, deps, "--request", writeRequest(t, validRequestDoc(t, nil)), "--jsonl")

	if res.exit != protocol.ExitOK {
		t.Fatalf("exit = %d, want 0\nstderr: %s", res.exit, res.stderr)
	}
	events := decodeStream(t, res.stdout, "job-1")
	if len(events) != 3 {
		t.Fatalf("got %d events, want accepted, segment, completed:\n%s", len(events), res.stdout)
	}
	accepted, ok := events[0].(*protocol.Accepted)
	if !ok {
		t.Fatalf("first event is %s", events[0].Type())
	}
	if accepted.Provider != "parakeet" || accepted.ModelID != "tdt-0.6b-v2" || accepted.ModelRevision != "157c157b" {
		t.Errorf("accepted reports %+v, want the resolved backend", accepted)
	}
	completed, ok := events[2].(*protocol.Completed)
	if !ok || completed.Outcome != protocol.OutcomeSpeech || completed.SegmentCount != 1 {
		t.Errorf("terminal event = %+v", events[2])
	}
	if counts.runs != 1 {
		t.Errorf("ran %d times", counts.runs)
	}
}

func TestTranscribeReportsNoSpeechAsSuccess(t *testing.T) {
	var counts transcribeDepsCounts
	deps := fakeTranscribeDeps(t, &counts)
	deps.run = func(ctx context.Context, _ transcription.Request, d transcription.Deps) (transcription.Summary, error) {
		if err := d.Sink.Emit(ctx, acceptedFrom(d.Info)); err != nil {
			return transcription.Summary{}, err
		}
		return transcription.Summary{DurationUS: 5_000_000, Outcome: transcription.OutcomeNoSpeech}, nil
	}
	res := runTranscribeCommand(t, deps, "--request", writeRequest(t, validRequestDoc(t, nil)), "--jsonl")

	if res.exit != protocol.ExitOK {
		t.Fatalf("exit = %d, want 0\nstderr: %s", res.exit, res.stderr)
	}
	events := decodeStream(t, res.stdout, "job-1")
	completed, ok := events[len(events)-1].(*protocol.Completed)
	if !ok || completed.Outcome != protocol.OutcomeNoSpeech || completed.SegmentCount != 0 {
		t.Fatalf("terminal event = %+v, want a no_speech completion", events[len(events)-1])
	}
}

func TestTranscribeFailures(t *testing.T) {
	cases := []struct {
		name string
		doc  string
		mut  func(*transcribeDeps)
		code string
		exit int
	}{
		{
			name: "unsupported timing",
			doc:  `{"protocol":"obey-transcript/v1","job_id":"job-1","input":{"path":"/a.wav","format":"wav_pcm_s16le","sample_rate_hz":16000,"channels":1,"timeline_origin_us":0},"timing":"word"}`,
			code: protocol.CodeUnsupportedCapability,
			exit: protocol.ExitInvalid,
		},
		{
			name: "input missing",
			mut: func(d *transcribeDeps) {
				d.openSource = func(string) (transcribeSource, error) { return nil, os.ErrNotExist }
			},
			code: protocol.CodeInputUnreadable,
			exit: protocol.ExitInvalid,
		},
		{
			name: "header disagrees with the request",
			mut: func(d *transcribeDeps) {
				d.openSource = func(string) (transcribeSource, error) {
					return &fakeTranscribeSource{format: audio.WAVFormat{SampleRate: 48000, Channels: 1, BitsPerSample: 16}}, nil
				}
			},
			code: protocol.CodeInputFormatMismatch,
			exit: protocol.ExitInvalid,
		},
		{
			name: "model not installed",
			mut: func(d *transcribeDeps) {
				d.manifest = func(*config.Config) (config.AssetManifest, error) {
					return config.AssetManifest{Schema: "obey-voice.assets.v1", Assets: []config.Asset{config.VADAsset()}}, nil
				}
			},
			code: protocol.CodeModelMissing,
			exit: protocol.ExitUnavailable,
		},
		{
			name: "provider cannot decode a file",
			mut: func(d *transcribeDeps) {
				d.newDecoder = func(*config.Config) (stt.SegmentDecoder, stt.DecoderInfo, func(), error) {
					return nil, stt.DecoderInfo{}, nil, stt.ErrSegmentDecodeUnsupported
				}
			},
			code: protocol.CodeProviderUnavailable,
			exit: protocol.ExitUnavailable,
		},
		{
			name: "decode fails mid job",
			mut: func(d *transcribeDeps) {
				d.run = func(ctx context.Context, _ transcription.Request, deps transcription.Deps) (transcription.Summary, error) {
					if err := deps.Sink.Emit(ctx, acceptedFrom(deps.Info)); err != nil {
						return transcription.Summary{}, err
					}
					return transcription.Summary{}, &transcription.Error{Code: "decode_failed", Retryable: true, Err: errors.New("recognizer returned nothing")}
				}
			},
			code: protocol.CodeDecodeFailed,
			exit: protocol.ExitRuntime,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var counts transcribeDepsCounts
			deps := fakeTranscribeDeps(t, &counts)
			if tc.mut != nil {
				tc.mut(&deps)
			}
			doc := tc.doc
			if doc == "" {
				doc = validRequestDoc(t, nil)
			}
			res := runTranscribeCommand(t, deps, "--request", writeRequest(t, doc), "--jsonl")

			if res.exit != tc.exit {
				t.Errorf("exit = %d, want %d\nstderr: %s", res.exit, tc.exit, res.stderr)
			}
			events := decodeStream(t, res.stdout, "job-1")
			if len(events) == 0 {
				t.Fatalf("no events on stdout; a job with an ID always gets a terminal event")
			}
			failed, ok := events[len(events)-1].(*protocol.Failed)
			if !ok {
				t.Fatalf("terminal event is %s, want failed", events[len(events)-1].Type())
			}
			if failed.Code != tc.code {
				t.Errorf("failed code = %s, want %s (%s)", failed.Code, tc.code, failed.Message)
			}
			if failed.Message == "" {
				t.Error("failed carries no human message")
			}
		})
	}
}

func TestTranscribeRejectsAnUnsupportedLanguageBeforeTouchingModels(t *testing.T) {
	var counts transcribeDepsCounts
	deps := fakeTranscribeDeps(t, &counts)
	doc := validRequestDoc(t, map[string]any{"language": "fr"})
	res := runTranscribeCommand(t, deps, "--request", writeRequest(t, doc), "--jsonl")

	if res.exit != protocol.ExitInvalid {
		t.Fatalf("exit = %d, want %d\nstderr: %s", res.exit, protocol.ExitInvalid, res.stderr)
	}
	events := decodeStream(t, res.stdout, "job-1")
	failed, ok := events[len(events)-1].(*protocol.Failed)
	if !ok || failed.Code != protocol.CodeUnsupportedCapability {
		t.Fatalf("terminal event = %+v, want %s", events[len(events)-1], protocol.CodeUnsupportedCapability)
	}
	if counts.decoders != 0 || counts.detectors != 0 {
		t.Errorf("built %d decoders and %d detectors for a request that cannot run", counts.decoders, counts.detectors)
	}
}

func TestTranscribeLanguagesReadsTheAssetTables(t *testing.T) {
	cases := []struct {
		name string
		cfg  config.Config
		want []string
		auto bool
	}{
		{"english parakeet", config.Config{ParakeetModel: "tdt-0.6b-v2"}, []string{"en"}, false},
		{"multilingual parakeet", config.Config{ParakeetModel: "tdt-0.6b-v3"}, nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := tc.cfg
			norm, err := config.NormalizeSTTWithMode("sherpa-parakeet", "")
			if err != nil {
				t.Fatal(err)
			}
			got, auto := transcribeLanguages(&cfg, norm)
			if auto != tc.auto || strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Fatalf("transcribeLanguages = %v, %v; want %v, %v", got, auto, tc.want, tc.auto)
			}
		})
	}
}

func TestTranscribeCancellationEmitsCancelledAndExits130(t *testing.T) {
	var counts transcribeDepsCounts
	deps := fakeTranscribeDeps(t, &counts)
	ctx, cancel := context.WithCancel(context.Background())
	deps.newContext = func() (context.Context, context.CancelFunc) { return ctx, cancel }
	seg := transcription.Segment{ID: "s1", StartUS: 800000, EndUS: 2400000, Text: "Here is the recording."}
	deps.run = func(ctx context.Context, _ transcription.Request, d transcription.Deps) (transcription.Summary, error) {
		if err := d.Sink.Emit(ctx, acceptedFrom(d.Info)); err != nil {
			return transcription.Summary{}, err
		}
		if err := d.Sink.Emit(ctx, seg); err != nil {
			return transcription.Summary{}, err
		}
		cancel()
		<-ctx.Done()
		return transcription.Summary{}, ctx.Err()
	}

	done := make(chan transcribeResult, 1)
	go func() {
		done <- runTranscribeCommand(t, deps, "--request", writeRequest(t, validRequestDoc(t, nil)), "--jsonl")
	}()

	var res transcribeResult
	select {
	case res = <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the command did not return within the 2 s cancellation budget")
	}

	if res.exit != protocol.ExitCancelled {
		t.Fatalf("exit = %d, want %d\nstderr: %s", res.exit, protocol.ExitCancelled, res.stderr)
	}
	events := decodeStream(t, res.stdout, "job-1")
	if len(events) < 2 {
		t.Fatalf("got %d events, want the emitted segment then cancelled:\n%s", len(events), res.stdout)
	}
	got, ok := events[len(events)-2].(*protocol.Segment)
	if !ok {
		t.Fatalf("event before terminal is %s, want the emitted segment", events[len(events)-2].Type())
	}
	if got.SegmentID != seg.ID || got.StartUS != seg.StartUS || got.EndUS != seg.EndUS || got.Text != seg.Text {
		t.Fatalf("segment = %+v, want the emitted utterance %+v", got, seg)
	}
	if _, ok := events[len(events)-1].(*protocol.Cancelled); !ok {
		t.Fatalf("terminal event is %s, want cancelled", events[len(events)-1].Type())
	}
	terminals := 0
	for _, ev := range events {
		if protocol.IsTerminal(ev.Type()) {
			terminals++
		}
		if ev.Type() == protocol.TypeCompleted {
			t.Fatal("a cancelled job reported completed")
		}
	}
	if terminals != 1 {
		t.Fatalf("stream has %d terminal events:\n%s", terminals, res.stdout)
	}
}

// TestTranscribeCancellationAfterAFailureKeepsOneTerminalEvent covers the
// order that breaks a naive implementation: the stream already ended in
// failed, and then the context turns out to have been cancelled.
func TestTranscribeCancellationAfterAFailureKeepsOneTerminalEvent(t *testing.T) {
	var counts transcribeDepsCounts
	deps := fakeTranscribeDeps(t, &counts)
	ctx, cancel := context.WithCancel(context.Background())
	deps.newContext = func() (context.Context, context.CancelFunc) { return ctx, cancel }
	deps.run = func(ctx context.Context, _ transcription.Request, d transcription.Deps) (transcription.Summary, error) {
		if err := d.Sink.Emit(ctx, acceptedFrom(d.Info)); err != nil {
			return transcription.Summary{}, err
		}
		// The encoder writes the terminal event, then the context is cancelled.
		failure := protocol.Errorf(protocol.CodeInternal, false, "a line exceeded the cap")
		if err := d.Sink.(*protocolSink).enc.Encode(protocol.NewFailedFor(failure)); err != nil {
			return transcription.Summary{}, err
		}
		cancel()
		return transcription.Summary{}, ctx.Err()
	}

	res := runTranscribeCommand(t, deps, "--request", writeRequest(t, validRequestDoc(t, nil)), "--jsonl")

	if res.exit != protocol.ExitCancelled {
		t.Fatalf("exit = %d, want %d", res.exit, protocol.ExitCancelled)
	}
	events := decodeStream(t, res.stdout, "job-1")
	terminals := 0
	for _, ev := range events {
		if protocol.IsTerminal(ev.Type()) {
			terminals++
		}
	}
	if terminals != 1 {
		t.Fatalf("stream has %d terminal events:\n%s", terminals, res.stdout)
	}
}
