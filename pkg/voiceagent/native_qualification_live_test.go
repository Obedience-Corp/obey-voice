package voiceagent

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/chatmedia"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/nativeaudio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/tts"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/tts/openaispeech"
)

// This is an explicit installed-runtime proof. It never installs a model,
// starts a daemon, opens a microphone/player or reads the regular config.
func TestLiveNativeProviderNegativePaths(t *testing.T) {
	output := os.Getenv("OBEY_NATIVE_NEGATIVE_RECEIPT")
	if output == "" {
		t.Skip("set an explicit fresh receipt path for local runtime negatives")
	}
	if !filepath.IsAbs(output) {
		t.Fatal("receipt must be an absolute fresh file")
	}
	file, err := os.OpenFile(output, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		t.Fatal(err)
	}
	receipt := map[string]any{"schema": 1, "scope": "actual selected local providers, software-only cancellation and invalid voice; not listening quality", "cases": map[string]any{}}
	defer func() {
		receipt["passed"] = !t.Failed()
		if err := json.NewEncoder(file).Encode(receipt); err != nil {
			t.Error(err)
		}
		if err := file.Sync(); err != nil {
			t.Error(err)
		}
		if err := file.Close(); err != nil {
			t.Error(err)
		}
	}()
	endpoint := os.Getenv("OBEY_NATIVE_NEGATIVE_ENDPOINT")
	if endpoint == "" {
		endpoint = "http://localhost:11434"
	}
	parsed, err := url.Parse(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	ip := net.ParseIP(parsed.Hostname())
	if parsed.Hostname() != "localhost" && (ip == nil || !ip.IsLoopback()) {
		t.Fatal("proof requires loopback runtime")
	}
	config.SetConfigDirForTest(t, t.TempDir())
	ctx, cancel := context.WithTimeout(t.Context(), 110*time.Second)
	defer cancel()
	cfg := &config.Config{BrainProvider: "ollama", OllamaHost: endpoint, OllamaModel: "lfm3-audio:2b", OllamaNumCtx: 32768, MaxHistory: 8, TTSProvider: config.OpenAISpeechProviderName, TTSFallbackProvider: "disabled", TTSLookahead: 1, OpenAISpeechRuntime: "external", OpenAISpeechExternal: true, OpenAISpeechURL: strings.TrimRight(endpoint, "/") + "/v1", OpenAISpeechModel: "lfm3-audio:2b", OpenAISpeechVoice: "female", OpenAISpeechTimeout: 20, ModelsDir: t.TempDir(), WorkDir: t.TempDir(), AgentName: "Samantha", Persona: "samantha"}
	media := chatmedia.NewAudioSession()
	defer media.Close()
	candidate, err := PrepareNativeCandidate(ctx, cfg, NativeCandidateOptions{Lifetime: ctx, Media: media, Cache: nativeaudio.NewSupportCache(), Controls: "conversation", Transcript: "off"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = candidate.Close() }()
	receipt["runtime"] = candidate.Setup.Metadata
	receipt["setup"] = candidate.Setup
	transport := &negativeTraceTransport{base: candidate.Policy.Client.HTTPClient().Transport}
	candidate.Policy.Client.HTTPClient().Transport = transport
	bus := events.NewBus()
	sink := &nativeDeliverySink{}
	agent, cleanup, err := candidate.NewAgent(ctx, Options{Events: bus, TextOnly: true, Player: sink, Worker: &stubBrain{}}, func(context.Context, *NativeCandidate) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	cases := receipt["cases"].(map[string]any)
	record := func(name string, values map[string]any) { cases[name] = values }

	t.Run("invalid_voice", func(t *testing.T) {
		bad := candidate.PreparedConfig()
		bad.OpenAISpeechVoice = "wi43b072-invalid-voice"
		provider, closeProvider, err := openaispeech.NewWithContext(ctx, bad, candidate.Policy.Client.HTTPClient())
		if err != nil {
			t.Fatal(err)
		}
		defer closeProvider()
		started := time.Now()
		before := transport.count("/v1/audio/speech")
		stream, err := provider.Synthesize(ctx, "Ready.")
		if err != nil {
			t.Fatal(err)
		}
		samples := 0
		for frames := range stream.Frames() {
			samples += len(frames)
		}
		failure := stream.Err()
		var typed *tts.ProviderError
		classified := errors.As(failure, &typed) && typed.Kind == tts.ProviderErrorInput
		record("invalid_voice", map[string]any{"elapsed_ms": time.Since(started).Milliseconds(), "speech_requests": transport.count("/v1/audio/speech") - before, "samples": samples, "classified_input": classified, "error": fmt.Sprint(failure)})
		if !classified || samples != 0 || transport.count("/v1/audio/speech")-before != 1 {
			t.Fatal("invalid voice not rejected once without audio", failure, samples)
		}
	})
	t.Run("cancel_before_http", func(t *testing.T) {
		turnCtx, stop := context.WithCancel(ctx)
		stop()
		before := transport.total.Load()
		stream, err := agent.TTS.Synthesize(turnCtx, "This request is already canceled.")
		if err != nil {
			t.Fatal(err)
		}
		for range stream.Frames() {
		}
		canceled := errors.Is(stream.Err(), context.Canceled)
		record("cancel_before_http", map[string]any{"http_requests": transport.total.Load() - before, "canceled": canceled})
		if !canceled || transport.total.Load() != before {
			t.Fatal("canceled request reached HTTP", stream.Err())
		}
	})
	t.Run("cancel_chat_delta", func(t *testing.T) {
		turnCtx, stop := context.WithCancel(ctx)
		defer stop()
		var deltas atomic.Int32
		events.Subscribe(bus, func(events.ResponseDelta) { deltas.Add(1); stop() })
		before := transport.count("/v1/audio/speech")
		pcmBefore := sink.frames.Load()
		started := time.Now()
		err := agent.SendText(turnCtx, "What is seven plus eight? Reply with only the number.")
		record("cancel_chat_delta", map[string]any{"elapsed_ms": time.Since(started).Milliseconds(), "response_deltas": deltas.Load(), "canceled": errors.Is(err, context.Canceled), "speech_requests": transport.count("/v1/audio/speech") - before, "authorized_pcm": sink.frames.Load() - pcmBefore, "error": fmt.Sprint(err)})
		if deltas.Load() == 0 || !errors.Is(err, context.Canceled) || transport.count("/v1/audio/speech") != before || sink.frames.Load() != pcmBefore {
			t.Fatal("chat cancel produced recovery/speech", err)
		}
	})
	t.Run("cancel_written_asr_request", func(t *testing.T) {
		ref, err := media.Import(ctx, nativeQualificationFixture)
		if err != nil {
			t.Fatal(err)
		}
		defer media.Release(ref)
		turnCtx, stop := context.WithCancel(ctx)
		defer stop()
		var wrote atomic.Bool
		turnCtx = httptrace.WithClientTrace(turnCtx, &httptrace.ClientTrace{WroteRequest: func(info httptrace.WroteRequestInfo) {
			if info.Err == nil {
				wrote.Store(true)
				stop()
			}
		}})
		started := time.Now()
		_, err = candidate.Policy.Client.Transcribe(turnCtx, ref, media)
		record("cancel_written_asr_request", map[string]any{"elapsed_ms": time.Since(started).Milliseconds(), "request_written": wrote.Load(), "canceled": errors.Is(err, context.Canceled), "error": fmt.Sprint(err), "milestone": "HTTP request written; no claim GPU inference began"})
		if !wrote.Load() || !errors.Is(err, context.Canceled) {
			t.Fatal("ASR cancellation not observed after request write", err)
		}
	})
	for _, mode := range []string{"after_first_pcm", "full_output_queue"} {
		t.Run(mode, func(t *testing.T) {
			turnCtx, stop := context.WithTimeout(ctx, 20*time.Second)
			defer stop()
			stream, err := agent.TTS.Synthesize(turnCtx, "This is an isolated software speech cancellation check. The original recording and the saved conversation remain unchanged. We are checking that cancellation closes the producer without playing any sound.")
			if err != nil {
				t.Fatal(err)
			}
			milestone := false
			first := 0
			if mode == "after_first_pcm" {
				select {
				case frame, ok := <-stream.Frames():
					milestone = ok && len(frame) > 0
					first = len(frame)
				case <-turnCtx.Done():
				}
			} else {
				ticker := time.NewTicker(time.Millisecond)
				defer ticker.Stop()
				for !milestone && turnCtx.Err() == nil {
					select {
					case <-ticker.C:
						milestone = len(stream.Frames()) == cap(stream.Frames())
					case <-turnCtx.Done():
					}
				}
			}
			queued := len(stream.Frames())
			started := time.Now()
			stop()
			drained := 0
			for frame := range stream.Frames() {
				drained += len(frame)
			}
			elapsed := time.Since(started)
			canceled := errors.Is(stream.Err(), context.Canceled)
			record(mode, map[string]any{"milestone_observed": milestone, "first_pcm_samples": first, "queued_batches_at_cancel": queued, "buffered_samples_drained_after_cancel": drained, "producer_join_ms": elapsed.Milliseconds(), "canceled": canceled, "authorized_playback_samples": 0, "error": fmt.Sprint(stream.Err())})
			if !milestone || !canceled || elapsed > 5*time.Second {
				t.Fatal("speech producer failed bounded milestone cancellation", milestone, stream.Err(), elapsed)
			}
		})
	}
	receipt["http_trace"] = transport.snapshot()
	if transport.active.Load() != 0 {
		t.Error("HTTP response body remained open")
	}
	if sink.frames.Load() != 0 {
		t.Error("canceled foreground admitted PCM")
	}
}

//go:embed nativeaudio/assets/setup.wav
var nativeQualificationFixture []byte

type negativeTraceTransport struct {
	base                   http.RoundTripper
	total, active, maximum atomic.Int32
	mu                     sync.Mutex
	paths                  map[string]int
}

func (t *negativeTraceTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	request = request.WithContext(httptrace.WithClientTrace(request.Context(), &httptrace.ClientTrace{WroteRequest: func(info httptrace.WroteRequestInfo) {
		if info.Err != nil {
			return
		}
		t.total.Add(1)
		t.mu.Lock()
		if t.paths == nil {
			t.paths = make(map[string]int)
		}
		t.paths[request.URL.Path]++
		t.mu.Unlock()
	}}))
	response, err := t.base.RoundTrip(request)
	if err != nil || response.Body == nil {
		return response, err
	}
	n := t.active.Add(1)
	for old := t.maximum.Load(); n > old && !t.maximum.CompareAndSwap(old, n); old = t.maximum.Load() {
	}
	response.Body = &negativeTraceBody{ReadCloser: response.Body, closed: func() { t.active.Add(-1) }}
	return response, nil
}
func (t *negativeTraceTransport) count(path string) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.paths[path]
}
func (t *negativeTraceTransport) snapshot() map[string]any {
	t.mu.Lock()
	defer t.mu.Unlock()
	return map[string]any{"requests": t.total.Load(), "paths": t.paths, "active_bodies": t.active.Load(), "maximum_bodies": t.maximum.Load()}
}
func (t *negativeTraceTransport) CloseIdleConnections() {
	if closer, ok := t.base.(interface{ CloseIdleConnections() }); ok {
		closer.CloseIdleConnections()
	}
}

type negativeTraceBody struct {
	io.ReadCloser
	once   sync.Once
	closed func()
}

func (b *negativeTraceBody) Close() error {
	err := b.ReadCloser.Close()
	b.once.Do(b.closed)
	return err
}

func TestNegativeTraceCountsWrittenRequestsAndOwnedBodyClose(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "fixture body") }))
	defer server.Close()
	transport := &negativeTraceTransport{base: &http.Transport{}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport}
	canceled, stop := context.WithCancel(t.Context())
	stop()
	request, err := http.NewRequestWithContext(canceled, http.MethodGet, server.URL+"/", nil)
	if err != nil {
		t.Fatal(err)
	}
	if response, err := client.Do(request); err == nil || response != nil || transport.total.Load() != 0 {
		t.Fatal("canceled request counted as written", response, err)
	}
	request, err = http.NewRequestWithContext(t.Context(), http.MethodGet, server.URL+"/", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	if transport.total.Load() != 1 || transport.active.Load() != 1 || transport.count("/") != 1 {
		t.Fatal(transport.snapshot())
	}
	if _, err := io.Copy(io.Discard, response.Body); err != nil {
		t.Fatal(err)
	}
	if err := response.Body.Close(); err != nil {
		t.Fatal(err)
	}
	if err := response.Body.Close(); err != nil {
		t.Fatal(err)
	}
	if transport.active.Load() != 0 || transport.maximum.Load() != 1 {
		t.Fatal("body lease miscounted", transport.snapshot())
	}
}
