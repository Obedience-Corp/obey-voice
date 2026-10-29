//go:build !integration

package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/stt"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/transcription"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/transcription/protocol"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWordRequestUsesRawDecoderAndPreservesMetadata(t *testing.T) {
	var counts transcribeDepsCounts
	deps := fakeTranscribeDeps(t, &counts)
	legacy := deps.newDecoder
	deps.newWordDecoder = legacy
	deps.newDecoder = func(*config.Config) (stt.SegmentDecoder, stt.DecoderInfo, func(), error) {
		t.Fatal("word mode used corrected decoder")
		return nil, stt.DecoderInfo{}, nil, nil
	}
	deps.run = func(ctx context.Context, req transcription.Request, d transcription.Deps) (transcription.Summary, error) {
		if req.Timing != protocol.TimingWord || d.MinSpeechSamples != 1 || d.SpeechFloor >= .002 {
			t.Fatal("word mode discarded short/quiet spans")
		}
		if err := d.Sink.Emit(ctx, acceptedFrom(d.Info)); err != nil {
			return transcription.Summary{}, err
		}
		err := d.Sink.Emit(ctx, transcription.Segment{ID: "s1", StartUS: 100000, EndUS: 500000, Text: "Um,", Words: []stt.TimedWord{{Text: "Um,", StartUS: 100000, EndUS: 300000}}})
		return transcription.Summary{DurationUS: 1000000, SegmentCount: 1, Outcome: transcription.OutcomeSpeech}, err
	}
	// fake configuration has no language; use an explicit request as the editor does.
	request := writeRequest(t, validRequestDoc(t, map[string]any{"protocol": protocol.WordProtocol, "timing": "word", "text_policy": "verbatim", "language": "en"}))
	cmd := newTranscribeCmd(deps)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"--request", request, "--jsonl"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	decoder := protocol.NewWordDecoder(&out, "job-1")
	ev, err := decoder.Next()
	if err != nil {
		t.Fatal(err)
	}
	a := ev.(*protocol.Accepted)
	if a.TextPolicy != "verbatim" || a.Language != "en" || a.TimingMethod != stt.WordTimingMethod {
		t.Fatalf("%+v", a)
	}
	ev, err = decoder.Next()
	if err != nil {
		t.Fatal(err)
	}
	if ev.(*protocol.Segment).Words[0].Text != "Um," {
		t.Fatal("filler changed")
	}
	if _, err = decoder.Next(); err != nil {
		t.Fatal(err)
	}
	if _, err = decoder.Next(); err != io.EOF {
		t.Fatal(err)
	}
}

func TestUnsupportedWordProviderCannotFallback(t *testing.T) {
	var counts transcribeDepsCounts
	deps := fakeTranscribeDeps(t, &counts)
	load := deps.loadConfig
	deps.loadConfig = func() (*config.Config, error) { cfg, err := load(); cfg.STTProvider = "whispercpp"; return cfg, err }
	deps.newWordDecoder = func(*config.Config) (stt.SegmentDecoder, stt.DecoderInfo, func(), error) {
		t.Fatal("unsupported provider constructed")
		return nil, stt.DecoderInfo{}, nil, nil
	}
	request := writeRequest(t, validRequestDoc(t, map[string]any{"protocol": protocol.WordProtocol, "timing": "word", "text_policy": "verbatim"}))
	cmd := newTranscribeCmd(deps)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"--request", request, "--jsonl"})
	if err := cmd.Execute(); err == nil {
		t.Fatal("unsupported timing succeeded")
	}
	ev, err := protocol.NewWordDecoder(&out, "job-1").Next()
	if err != nil {
		t.Fatal(err)
	}
	if ev.(*protocol.Failed).Code != protocol.CodeUnsupportedCapability || counts.decoders != 0 || counts.runs != 0 {
		t.Fatalf("fallback or wrong failure: %+v %+v", ev, counts)
	}
}

func TestExplicitSpeechProcessIgnoresGlobalProviderAndNeverDownloads(t *testing.T) {
	if testing.Short() {
		t.Skip("builds real CLI")
	}
	dir := t.TempDir()
	models := filepath.Join(dir, "editor-models")
	cfg := filepath.Join(dir, "speech.json")
	doc := map[string]string{"schema": "obey-voice.speech-config/v1", "provider": "parakeet", "model": "tdt-0.6b-v2", "language": "en", "models_dir": models}
	data, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg, data, 0600); err != nil {
		t.Fatal(err)
	}
	env := append(os.Environ(), "STT_PROVIDER=unsupported-global-provider", "MODELS_DIR="+filepath.Join(dir, "wrong-models"))
	caps := runTranscribeProcess(t, env, "transcribe", "--speech-config", cfg, "capabilities", "--json")
	if caps.exit != 0 {
		t.Fatal(caps.stderr)
	}
	var got protocol.Capabilities
	if err := json.Unmarshal([]byte(caps.stdout), &got); err != nil {
		t.Fatal(err)
	}
	if got.Readiness.Ready || got.Readiness.ModelsDir != models || !got.WordAnalysis.Supported {
		t.Fatalf("%+v", got)
	}
	wav := filepath.Join(dir, "input.wav")
	writeTranscribeWAV(t, wav, 16000, 1, 16000)
	request := writeProcessRequest(t, dir, "isolated", wav, map[string]any{"protocol": protocol.WordProtocol, "timing": "word", "text_policy": "verbatim", "language": "en"})
	result := runTranscribeProcess(t, env, "transcribe", "--speech-config", cfg, "--request", request, "--jsonl")
	event, err := protocol.NewWordDecoder(strings.NewReader(result.stdout), "isolated").Next()
	if err != nil {
		t.Fatal(err)
	}
	failed, ok := event.(*protocol.Failed)
	if !ok || result.exit != 3 || failed.Code != protocol.CodeModelMissing {
		t.Fatalf("%+v %+v", result, event)
	}
	if _, err := os.Stat(models); !os.IsNotExist(err) {
		t.Fatal("read-only job created models directory", err)
	}
	setup := runTranscribeProcess(t, env, "transcribe", "setup")
	if setup.exit == 0 || !strings.Contains(setup.stderr, "requires --speech-config") {
		t.Fatalf("%+v", setup)
	}
}
