package openaispeech

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/tts"
)

// fakeServer imitates Ollama's speech surface: GET /models, POST
// /audio/speech streaming pcm in the chunk sizes the test chooses, a 400 that
// names the voices for an unknown one, and no /audio/voices.
type fakeServer struct {
	t        *testing.T
	models   []string
	voices   []string
	chunks   []int // byte sizes to flush per write; cycles
	samples  int   // samples per request
	mu       sync.Mutex
	requests []speechRequest
	voiceAPI []string // when non-nil, GET /audio/voices answers with these
	hold     chan struct{}
	// holdAfterFirst blocks the body after the first flushed chunk, so a test
	// can cancel while the decode loop is mid-stream.
	holdAfterFirst chan struct{}
	fail500        bool
	// failStatus, when non-zero, answers every speech request with that
	// status and an OpenAI error envelope.
	failStatus int
}

func (f *fakeServer) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/models", func(w http.ResponseWriter, r *http.Request) {
		type m struct {
			ID string `json:"id"`
		}
		out := struct {
			Data []m `json:"data"`
		}{}
		for _, id := range f.models {
			out.Data = append(out.Data, m{ID: id})
		}
		_ = json.NewEncoder(w).Encode(out)
	})
	mux.HandleFunc("/v1/audio/voices", func(w http.ResponseWriter, r *http.Request) {
		if f.voiceAPI == nil {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"voices": f.voiceAPI})
	})
	mux.HandleFunc("/v1/audio/speech", func(w http.ResponseWriter, r *http.Request) {
		var req speechRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		f.mu.Lock()
		f.requests = append(f.requests, req)
		f.mu.Unlock()
		known := false
		for _, v := range f.voices {
			if v == req.Voice {
				known = true
			}
		}
		if !known {
			w.WriteHeader(400)
			_, _ = w.Write([]byte(`{"error":{"message":"voice \"` + req.Voice + `\" not found; available voices: ` + strings.Join(f.voices, ", ") + `","type":"invalid_request_error"}}`))
			return
		}
		if f.fail500 {
			w.WriteHeader(500)
			_, _ = w.Write([]byte(`{"error":{"message":"runner crashed"}}`))
			return
		}
		if f.failStatus != 0 {
			w.WriteHeader(f.failStatus)
			_, _ = w.Write([]byte(`{"error":{"message":"` + http.StatusText(f.failStatus) + `"}}`))
			return
		}
		if f.hold != nil {
			select {
			case <-f.hold:
			case <-r.Context().Done():
				return
			}
		}
		w.Header().Set("Content-Type", "audio/pcm")
		w.WriteHeader(200)
		fl, _ := w.(http.Flusher)
		body := make([]byte, f.samples*2)
		for i := 0; i < f.samples; i++ {
			binary.LittleEndian.PutUint16(body[2*i:], uint16(int16(i%2000-1000)))
		}
		ci := 0
		for len(body) > 0 {
			n := f.chunks[ci%len(f.chunks)]
			if n > len(body) {
				n = len(body)
			}
			if _, err := w.Write(body[:n]); err != nil {
				return
			}
			if fl != nil {
				fl.Flush()
			}
			body = body[n:]
			if ci == 0 && f.holdAfterFirst != nil {
				select {
				case <-f.holdAfterFirst:
				case <-r.Context().Done():
					return
				}
			}
			ci++
		}
	})
	return mux
}

func newFake(t *testing.T) (*fakeServer, *httptest.Server) {
	t.Helper()
	f := &fakeServer{t: t, models: []string{"phonon:latest", "dolphin-mistral:latest"}, voices: []string{"default"}, chunks: []int{4096, 4095, 1, 777}, samples: 24000}
	srv := httptest.NewServer(f.handler())
	t.Cleanup(srv.Close)
	return f, srv
}

func cfgFor(url string) *config.Config {
	return &config.Config{TTSProvider: config.OpenAISpeechProviderName, OpenAISpeechURL: url + "/v1/", OpenAISpeechModel: "phonon", OpenAISpeechVoice: "default"}
}

func drain(t *testing.T, p *Provider, text string) ([]float32, error) {
	t.Helper()
	stream, err := p.Synthesize(context.Background(), text)
	if err != nil {
		return nil, err
	}
	var got []float32
	for frame := range stream.Frames() {
		got = append(got, frame...)
	}
	return got, stream.Err()
}

func TestSynthesizeStreamsPCM16WithOddChunks(t *testing.T) {
	f, srv := newFake(t)
	p, cleanup, err := New(cfgFor(srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if !p.Available() || p.ProviderName() != "openai-speech" || p.ModelTier() != "phonon" {
		t.Fatalf("provider state: %+v", p.Status())
	}
	got, err := drain(t, p, "Hello there. This is a test.")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != f.samples {
		t.Fatalf("decoded %d samples, want %d (odd chunk boundary lost a byte?)", len(got), f.samples)
	}
	// Sample i encodes i%2000-1000: spot-check that alignment survived the
	// odd-length chunks the server flushed.
	for _, i := range []int{0, 1, 2047, 2048, 4095, 4096, 23999} {
		want := float32(int16(i%2000-1000)) / 32768
		if got[i] != want {
			t.Fatalf("sample %d = %v, want %v: byte alignment broke", i, got[i], want)
		}
	}
	if len(f.requests) != 1 || f.requests[0].ResponseFormat != "pcm" || f.requests[0].Model != "phonon" || f.requests[0].Voice != "default" {
		t.Fatalf("request = %+v", f.requests)
	}
	caps := p.Capabilities()
	if !caps.SupportsStreaming || caps.SampleRates[0] != SampleRate || len(caps.Modes) != 1 || caps.Modes[0].Voices[0].Name != "default" {
		t.Fatalf("caps = %+v", caps)
	}
}

func TestUnknownVoiceSurfacesServerMessage(t *testing.T) {
	_, srv := newFake(t)
	p, _, err := New(cfgFor(srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	result, err := p.SynthesizeRequest(context.Background(), tts.SynthesisRequest{Text: "hi", Voice: "nope"})
	if err != nil {
		t.Fatal(err)
	}
	for range result.Stream.Frames() {
	}
	err = result.Stream.Err()
	if err == nil || !strings.Contains(err.Error(), "available voices: default") {
		t.Fatalf("err = %v, want the server's voice list", err)
	}
	// A 4xx is this request's fault: not something the Kokoro fallback should hide.
	if !tts.IsProviderErrorKind(err, tts.ProviderErrorInput) {
		t.Fatalf("400 must classify as input, got %v", err)
	}
}

// The pipeline's fallback predicate only acts on typed ProviderErrors of kind
// Unavailable, Worker or Malformed, and never on Canceled. Every failure path
// here has to come out typed or the Kokoro fallback NewTTSSet built never runs.
func TestFailuresAreTypedForTheFallbackPredicate(t *testing.T) {
	_, _, err := New(cfgFor("http://127.0.0.1:1"))
	if !tts.IsProviderErrorKind(err, tts.ProviderErrorUnavailable) || !errors.Is(err, tts.ErrProviderUnavailable) {
		t.Fatalf("connection refused at probe: %v", err)
	}

	_, srv := newFake(t)
	p, _, err := New(cfgFor(srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	// Server dies after construction: the request fails at the transport.
	srv.Close()
	got, err := drain(t, p, "Is anyone there?")
	if len(got) != 0 || !tts.IsProviderErrorKind(err, tts.ProviderErrorUnavailable) {
		t.Fatalf("transport failure mid-conversation: samples=%d err=%v", len(got), err)
	}

	// 5xx is the server's fault.
	f2, srv2 := newFake(t)
	f2.fail500 = true
	p2, _, err := New(cfgFor(srv2.URL))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := drain(t, p2, "hello"); !tts.IsProviderErrorKind(err, tts.ProviderErrorUnavailable) {
		t.Fatalf("500: %v", err)
	}

	// Per-request timeout is a worker fault (fallback), not a turn cancel.
	f3, srv3 := newFake(t)
	f3.hold = make(chan struct{})
	defer close(f3.hold)
	cfg3 := cfgFor(srv3.URL)
	cfg3.OpenAISpeechTimeout = 1
	p3, _, err := New(cfg3)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := drain(t, p3, "slow"); !tts.IsProviderErrorKind(err, tts.ProviderErrorWorker) || !strings.Contains(err.Error(), "timed out after 1s") {
		t.Fatalf("timeout: %v", err)
	}

	// Turn cancel is Canceled: the fallback must not speak over a cancelled turn.
	f4, srv4 := newFake(t)
	f4.hold = make(chan struct{})
	p4, _, err := New(cfgFor(srv4.URL))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	stream, err := p4.Synthesize(ctx, "cancel me")
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	close(f4.hold)
	for range stream.Frames() {
	}
	if !tts.IsProviderErrorKind(stream.Err(), tts.ProviderErrorCanceled) {
		t.Fatalf("cancel: %v", stream.Err())
	}
}

// A server that times out (408) or refuses the load (429) is not answering
// this request's content; shouldFallback must get a kind it acts on, or the
// configured Kokoro fallback is bypassed and the turn fails. Permanent
// request errors stay Input so a bad voice is reported, not spoken over.
func TestTransientStatusesAreFallbackEligible(t *testing.T) {
	cases := []struct {
		status int
		kind   tts.ProviderErrorKind
	}{
		{http.StatusRequestTimeout, tts.ProviderErrorWorker},
		{http.StatusTooManyRequests, tts.ProviderErrorUnavailable},
		{http.StatusServiceUnavailable, tts.ProviderErrorUnavailable},
		{http.StatusNotFound, tts.ProviderErrorInput},
		{http.StatusUnauthorized, tts.ProviderErrorInput},
		{http.StatusUnprocessableEntity, tts.ProviderErrorInput},
	}
	for _, c := range cases {
		f, srv := newFake(t)
		f.failStatus = c.status
		p, _, err := New(cfgFor(srv.URL))
		if err != nil {
			t.Fatal(err)
		}
		got, err := drain(t, p, "hello")
		if len(got) != 0 || !tts.IsProviderErrorKind(err, c.kind) || !strings.Contains(err.Error(), http.StatusText(c.status)) {
			t.Errorf("%d: samples=%d kind want %s, err=%v", c.status, len(got), c.kind, err)
		}
	}
}

func TestLongTextIsSplitUnderTheCap(t *testing.T) {
	f, srv := newFake(t)
	f.samples = 100
	p, _, err := New(cfgFor(srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	sentence := strings.Repeat("word ", 199) + "end. " // 1000 chars
	text := strings.Repeat(sentence, 5)                // 5000 chars
	got, err := drain(t, p, text)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.requests) != 2 {
		t.Fatalf("%d requests, want 2 for %d chars", len(f.requests), len(text))
	}
	for _, r := range f.requests {
		if len(r.Input) > MaxInputChars || !strings.HasSuffix(strings.TrimSpace(r.Input), "end.") {
			t.Fatalf("piece of %d chars does not end on a sentence: %q", len(r.Input), r.Input[len(r.Input)-20:])
		}
	}
	if len(got) != 2*f.samples {
		t.Fatalf("decoded %d samples, want %d", len(got), 2*f.samples)
	}
}

func TestSplitForLimit(t *testing.T) {
	if got := SplitForLimit("", 10); got != nil {
		t.Fatal(got)
	}
	if got := SplitForLimit("short", 10); len(got) != 1 || got[0] != "short" {
		t.Fatal(got)
	}
	got := SplitForLimit("One two. Three four! Five six? Seven", 12)
	want := []string{"One two.", "Three four!", "Five six?", "Seven"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("got %q", got)
	}
	// No separators at all: a hard cut rather than an oversized request.
	for _, piece := range SplitForLimit(strings.Repeat("x", 25), 10) {
		if len(piece) > 10 {
			t.Fatal(piece)
		}
	}
	// Multibyte text: the cap counts runes and a hard cut never lands inside
	// one, so no piece carries U+FFFD after a JSON round trip.
	text := strings.Repeat("日本語のテキスト", 10) // 80 runes, 240 bytes, no separators
	pieces := SplitForLimit(text, 12)
	if len(pieces) != 7 {
		t.Fatalf("%d pieces for 80 runes at 12 per piece", len(pieces))
	}
	var rejoined string
	for _, piece := range pieces {
		if !utf8.ValidString(piece) || utf8.RuneCountInString(piece) > 12 {
			t.Fatalf("piece %q is not valid UTF-8 within the rune cap", piece)
		}
		b, _ := json.Marshal(piece)
		if strings.Contains(string(b), "\\ufffd") || strings.ContainsRune(string(b), '\uFFFD') {
			t.Fatalf("piece %q corrupted by JSON encoding", piece)
		}
		rejoined += piece
	}
	if rejoined != text {
		t.Fatal("pieces do not rejoin to the original text")
	}
	// Sentence separators found by rune position, not byte position.
	got = SplitForLimit("¡Hola! ¿Qué tal? Bien.", 10)
	if strings.Join(got, "|") != "¡Hola!|¿Qué tal?|Bien." {
		t.Fatalf("got %q", got)
	}
}

func TestEmptyAfterCleaningClosesWithoutRequest(t *testing.T) {
	f, srv := newFake(t)
	p, _, err := New(cfgFor(srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	got, err := drain(t, p, "🎉")
	if err != nil || len(got) != 0 || len(f.requests) != 0 {
		t.Fatalf("got %d samples, %d requests, err %v", len(got), len(f.requests), err)
	}
}

func TestUnreachableServerIsProviderUnavailable(t *testing.T) {
	_, _, err := New(cfgFor("http://127.0.0.1:1"))
	if !errors.Is(err, tts.ErrProviderUnavailable) || !strings.Contains(err.Error(), "127.0.0.1:1") || !strings.Contains(err.Error(), "ollama pull phonon") {
		t.Fatalf("err = %v", err)
	}
}

func TestMissingModelNamesWhatTheServerHas(t *testing.T) {
	f, srv := newFake(t)
	f.models = []string{"dolphin-mistral:latest"}
	_, _, err := New(cfgFor(srv.URL))
	if err == nil || !strings.Contains(err.Error(), `"phonon"`) || !strings.Contains(err.Error(), "dolphin-mistral:latest") {
		t.Fatalf("err = %v", err)
	}
	// A server that lists nothing (not a model registry) is not refused.
	f.models = nil
	if _, _, err := New(cfgFor(srv.URL)); err != nil {
		t.Fatal(err)
	}
}

func TestVoiceCatalogFromServerWhenOffered(t *testing.T) {
	f, srv := newFake(t)
	f.voiceAPI = []string{"Freya", "Harper", "default"}
	p, _, err := New(cfgFor(srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	names := []string{}
	for _, v := range p.ListVoices("", "") {
		names = append(names, v.Name)
	}
	if strings.Join(names, ",") != "Freya,Harper,default" {
		t.Fatalf("voices = %v", names)
	}
	for _, body := range []string{`["a","b"]`, `{"voices":[{"name":"a"},{"id":"b"}]}`, `{"data":[{"id":"a"},{"id":"b"}]}`} {
		if got := voiceNames([]byte(body)); strings.Join(got, ",") != "a,b" {
			t.Fatalf("voiceNames(%s) = %v", body, got)
		}
	}
}

func TestCancelMidBodyStopsTheStream(t *testing.T) {
	f, srv := newFake(t)
	f.hold = make(chan struct{})
	p, _, err := New(cfgFor(srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	stream, err := p.Synthesize(ctx, "Waiting on the server.")
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	close(f.hold)
	done := make(chan struct{})
	go func() {
		for range stream.Frames() {
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("stream never closed after cancel")
	}
	if !errors.Is(stream.Err(), context.Canceled) {
		t.Fatalf("stream err = %v", stream.Err())
	}
}

func TestCancelMidBodyStopsTheDecodeLoop(t *testing.T) {
	f, srv := newFake(t)
	f.holdAfterFirst = make(chan struct{})
	f.samples = 48000
	p, _, err := New(cfgFor(srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	stream, err := p.Synthesize(ctx, "Half way through the body.")
	if err != nil {
		t.Fatal(err)
	}
	// The first chunk has to arrive before the cancel: that is what puts the
	// provider inside decodePCM16 rather than inside client.Do.
	first := <-stream.Frames()
	if len(first) == 0 {
		t.Fatal("no first frame")
	}
	cancel()
	done := make(chan struct{})
	go func() {
		for range stream.Frames() {
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("stream never closed after a mid-body cancel")
	}
	if !errors.Is(stream.Err(), context.Canceled) {
		t.Fatalf("stream err = %v", stream.Err())
	}
	close(f.holdAfterFirst)
}

func TestGraceCoversAColdModelLoad(t *testing.T) {
	p := &Provider{}
	if g := p.FirstAudioGraceForChars(17); g < 5*time.Second {
		t.Fatalf("short segment grace %v is below the measured 2.4 s reload", g)
	}
	if g := p.FirstAudioGraceForChars(100000); g != maxFirstAudioGrace {
		t.Fatalf("grace %v not capped", g)
	}
	if p.FirstAudioGrace() != p.FirstAudioGraceForChars(tts.TypicalSegmentChars) {
		t.Fatal("default grace must use the typical segment")
	}
}

func TestDecodeCarriesOddByte(t *testing.T) {
	// 3 samples delivered as 1+2+3 bytes: every split lands mid-sample.
	raw := []byte{0x00, 0x01, 0x00, 0x02, 0x00, 0x03}
	r := &chunkReader{data: raw, sizes: []int{1, 2, 3}}
	ctx := context.Background()
	stream := newTestStream(ctx)
	if err := decodePCM16(r, stream); err != nil {
		t.Fatal(err)
	}
	stream.Close()
	var got []float32
	for frame := range stream.Frames() {
		got = append(got, frame...)
	}
	want := []float32{256.0 / 32768, 512.0 / 32768, 768.0 / 32768}
	if len(got) != 3 || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Fatalf("got %v want %v", got, want)
	}
}

type chunkReader struct {
	data  []byte
	sizes []int
	i     int
}

func (c *chunkReader) Read(p []byte) (int, error) {
	if len(c.data) == 0 {
		return 0, io.EOF
	}
	n := c.sizes[c.i%len(c.sizes)]
	c.i++
	if n > len(c.data) {
		n = len(c.data)
	}
	if n > len(p) {
		n = len(p)
	}
	copy(p, c.data[:n])
	c.data = c.data[n:]
	return n, nil
}

type collectWriter struct{ got []float32 }

func (c *collectWriter) Write(s []float32) error { c.got = append(c.got, s...); return nil }

func TestTailTrimmerDropsPadKeepsPauses(t *testing.T) {
	tone := func(n int) []float32 {
		out := make([]float32, n)
		for i := range out {
			out[i] = 0.3
		}
		return out
	}
	silence := func(n int) []float32 { return make([]float32, n) }

	// Speech, a 300 ms pause, speech, then a 700 ms Phonon pad, in uneven chunks.
	var body []float32
	body = append(body, tone(4000)...)
	body = append(body, silence(SampleRate*300/1000)...)
	body = append(body, tone(4000)...)
	body = append(body, silence(SampleRate*700/1000)...)
	c := &collectWriter{}
	tr := newTailTrimmer(c)
	for i, n := 0, 0; i < len(body); i += n {
		n = 1500 + (i/1500%3)*700
		if i+n > len(body) {
			n = len(body) - i
		}
		if err := tr.Write(body[i : i+n]); err != nil {
			t.Fatal(err)
		}
	}
	if err := tr.finish(); err != nil {
		t.Fatal(err)
	}
	want := 4000 + SampleRate*300/1000 + 4000 + tailKeepSamples
	if len(c.got) != want {
		t.Fatalf("kept %d samples, want %d (speech + pause + speech + %d keep)", len(c.got), want, tailKeepSamples)
	}
	// The pause survived in place.
	if c.got[4000+100] != 0 || c.got[4000+SampleRate*300/1000+10] != 0.3 {
		t.Fatal("mid-utterance pause was not preserved")
	}
	// All-silent input emits nothing.
	c2 := &collectWriter{}
	tr2 := newTailTrimmer(c2)
	_ = tr2.Write(silence(5000))
	_ = tr2.finish()
	if len(c2.got) != 0 {
		t.Fatalf("silent input emitted %d samples", len(c2.got))
	}
}
