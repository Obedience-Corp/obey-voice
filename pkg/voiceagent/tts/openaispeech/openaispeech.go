// Package openaispeech speaks through any server that implements the
// OpenAI-compatible POST /v1/audio/speech endpoint and can stream raw PCM.
//
// Ollama 0.40.9+ serves Gradium's Phonon this way on Apple Silicon, and
// Gradium's ptts-openai-server exposes the same endpoint with its full voice
// catalog. The client asks for response_format "pcm" (16-bit little-endian
// mono, 24 kHz) and feeds frames to the player as the body arrives, so first
// audio does not wait for the whole utterance.
package openaispeech

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/phonon"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/textclean"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/tts"
)

// SampleRate is what the pcm response format carries.
const SampleRate = 24000

// MaxInputChars is Ollama's cap on one request's input. Longer text is split
// at sentence boundaries and requested in order into the same stream.
const MaxInputChars = 4096

// probeTimeout bounds the construction-time GET /models.
const probeTimeout = 3 * time.Second

// Provider is the OpenAI-compatible speech client.
type Provider struct {
	base    string
	model   string
	voice   string
	apiKey  string
	timeout time.Duration
	client  *http.Client

	alive  atomic.Bool
	detail string
	// voices is the catalog: the server's GET /audio/voices when it offers
	// one (the ptts server does), Ollama's /api/show voices when the target
	// is Ollama and its release lists them, else the configured voice.
	voices []tts.Voice
	// catalogSource says which of those answered: "server", "ollama" or
	// "configured". notice explains a "configured" answer the user can fix.
	catalogSource string
	notice        string
	// modelsDir is where voices.jsonl, the descriptive catalog, is looked for.
	modelsDir  string
	native     *phonon.Process
	nativeSlot chan struct{}
}

// New constructs the provider after proving the server answers and, when it
// lists models, that the configured model is among them. A server that is not
// there is tts.ErrProviderUnavailable so the caller can say so rather than
// fail on the first sentence.
func New(cfg *config.Config) (*Provider, func(), error) {
	if err := config.ValidateOpenAISpeechRuntime(cfg); err != nil {
		return nil, nil, err
	}
	var native *phonon.Process
	if config.NativePhononSelected(cfg) {
		o, err := config.NativePhononOptions(cfg)
		if err != nil {
			return nil, nil, err
		}
		if !phonon.Installed(o) {
			return nil, nil, fmt.Errorf("native Phonon assets missing; run `obey-voice models ensure --tts`")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		native, err = phonon.Start(ctx, o, true)
		cancel()
		if err != nil {
			return nil, nil, err
		}
		next := *cfg
		next.OpenAISpeechRuntime, next.OpenAISpeechURL, next.OpenAISpeechModel = "external", native.URL, phonon.Model
		cfg = &next
	}
	p := &Provider{
		native:  native,
		base:    config.OpenAISpeechURL(cfg),
		model:   config.OpenAISpeechModel(cfg),
		voice:   config.TTSVoiceFor(cfg, config.OpenAISpeechProviderName),
		timeout: time.Duration(config.DefaultOpenAISpeechTimeout) * time.Second,
		client:  &http.Client{},
	}
	if cfg != nil {
		p.apiKey = strings.TrimSpace(cfg.OpenAISpeechAPIKey)
		if cfg.OpenAISpeechTimeout > 0 {
			p.timeout = time.Duration(cfg.OpenAISpeechTimeout) * time.Second
		}
	}
	if p.voice == "" {
		p.voice = config.DefaultOpenAISpeechVoice
	}
	if cfg != nil {
		p.modelsDir = config.ModelsDirFrom(cfg)
	}
	if err := p.probe(context.Background()); err != nil {
		if native != nil {
			native.Close()
		}
		return nil, nil, err
	}
	if native != nil {
		p.nativeSlot = make(chan struct{}, 1)
		p.detail += "; " + native.Device
	}
	p.alive.Store(true)
	return p, p.Delete, nil
}

// probe checks the server and the model offline from any synthesis.
func (p *Provider) probe(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.base+"/models", nil)
	if err != nil {
		return err
	}
	p.auth(req)
	resp, err := p.client.Do(req)
	if err != nil {
		return p.fail("probe", tts.ProviderErrorUnavailable,
			fmt.Errorf("%s did not answer (%v); for Phonon start Ollama 0.40.9+ and run `ollama pull phonon`", p.base, unwrapNet(err)))
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return p.fail("probe", tts.ProviderErrorUnavailable, fmt.Errorf("%s/models returned %d: %s", p.base, resp.StatusCode, errorMessage(body, resp.Status)))
	}
	if ids := modelIDs(body); len(ids) > 0 && !hasModel(ids, p.model) {
		return p.fail("probe", tts.ProviderErrorUnavailable, fmt.Errorf("model %q is not served by %s (it offers %s); set openai_speech_model to an advertised model ID, or run setup for the selected server",
			p.model, p.base, strings.Join(ids, ", ")))
	}
	p.detail = p.model + " at " + p.base
	p.loadCatalog(ctx)
	return nil
}

func (p *Provider) auth(req *http.Request) {
	if p.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.apiKey)
	}
}

// fetchServerVoices asks for the catalog a ptts-style server publishes. Any
// failure, including Ollama's 404, reports no catalog.
func (p *Provider) fetchServerVoices(ctx context.Context) ([]string, bool) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.base+"/audio/voices", nil)
	if err != nil {
		return nil, false
	}
	p.auth(req)
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, false
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	names := voiceNames(body)
	return names, len(names) > 0
}

// Synthesize streams PCM for text in the configured voice.
func (p *Provider) Synthesize(ctx context.Context, text string) (*audio.PCMStream, error) {
	result, err := p.SynthesizeRequest(ctx, tts.SynthesisRequest{Text: text})
	if err != nil {
		return nil, err
	}
	return result.Stream, nil
}

// SynthesizeRequest streams PCM for a typed request. Empty Voice uses the
// configured one; a non-native sample rate is an error because the server
// decides the rate.
func (p *Provider) SynthesizeRequest(ctx context.Context, req tts.SynthesisRequest) (tts.SynthesisResult, error) {
	if !p.alive.Load() {
		return tts.SynthesisResult{}, p.fail("synthesize", tts.ProviderErrorUnavailable, errors.New("provider is closed"))
	}
	voice := p.voice
	if v := strings.TrimSpace(req.Voice); v != "" {
		voice = v
	}
	if req.SampleRate != 0 && req.SampleRate != SampleRate {
		return tts.SynthesisResult{}, p.fail("synthesize", tts.ProviderErrorInput, fmt.Errorf("cannot resample to %d Hz (native rate %d Hz)", req.SampleRate, SampleRate))
	}
	stream := audio.NewPCMStream(ctx)
	// Frames arrive while the server generates, so the player may start on the
	// first ones instead of waiting for the end of the body.
	stream.SetIncremental(true)

	go func() {
		defer func() {
			if r := recover(); r != nil {
				stream.CloseWithError(fmt.Errorf("openai-speech panic: %v", r))
			}
		}()
		if err := stream.SetSampleRate(SampleRate); err != nil {
			stream.CloseWithError(err)
			return
		}
		prepared := textclean.PrepareSpokenText(req.Text)
		audio.RecordDebugSynthesis(config.OpenAISpeechProviderName, req.Text, prepared)
		if strings.TrimSpace(prepared) == "" {
			stream.Close()
			return
		}
		for _, piece := range SplitForLimit(prepared, MaxInputChars) {
			if err := p.speakInto(ctx, stream, piece, voice); err != nil {
				stream.CloseWithError(err)
				return
			}
		}
		stream.Close()
	}()

	return tts.SynthesisResult{
		Stream:     stream,
		SampleRate: SampleRate,
		Provider:   config.OpenAISpeechProviderName,
		Model:      p.model,
		Voice:      voice,
	}, nil
}

type speechRequest struct {
	Model          string `json:"model"`
	Input          string `json:"input"`
	Voice          string `json:"voice"`
	ResponseFormat string `json:"response_format"`
}

// speakInto makes one /audio/speech request and writes its PCM into stream.
func (p *Provider) speakInto(ctx context.Context, stream *audio.PCMStream, text, voice string) error {
	turnCtx := ctx
	ctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()
	if p.nativeSlot != nil {
		select {
		case p.nativeSlot <- struct{}{}:
			defer func() { <-p.nativeSlot }()
		case <-ctx.Done():
			return p.classify(turnCtx, ctx, "queue", ctx.Err())
		}
	}
	payload, err := json.Marshal(speechRequest{Model: p.model, Input: text, Voice: voice, ResponseFormat: "pcm"})
	if err != nil {
		return err
	}
	var resp *http.Response
	for attempt := 0; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.base+"/audio/speech", bytes.NewReader(payload))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		p.auth(req)
		resp, err = p.client.Do(req)
		if err != nil {
			return p.classify(turnCtx, ctx, "request", fmt.Errorf("%s did not answer: %w", p.base, unwrapNet(err)))
		}
		// The owned server releases its generation slot just after the body
		// ends (including disconnect). Retry only that pre-audio busy response,
		// for at most one second. External-server 429 behavior is unchanged.
		if p.nativeSlot == nil || resp.StatusCode != http.StatusTooManyRequests || attempt >= 20 {
			break
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		resp.Body.Close()
		timer := time.NewTimer(50 * time.Millisecond)
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			return p.classify(turnCtx, ctx, "queue", ctx.Err())
		}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		return p.fail("request", statusKind(resp.StatusCode), fmt.Errorf("%s: %s", resp.Status, errorMessage(body, "no error body")))
	}
	trim := newTailTrimmer(stream)
	if err := decodePCM16(resp.Body, trim); err != nil {
		return p.classify(turnCtx, ctx, "stream", err)
	}
	return trim.finish()
}

// statusKind classifies a non-200 answer for the pipeline's fallback
// predicate. A permanent 4xx (unknown voice, text too long) is about this
// request and must surface rather than be papered over by the fallback voice.
// A 408 is the server giving up on the request, a 429 is the server refusing
// to take it right now, and any 5xx is the server itself: none of those is
// something a retry with the same text would fix, and all are exactly what the
// configured fallback exists for.
func statusKind(status int) tts.ProviderErrorKind {
	switch {
	case status == http.StatusRequestTimeout:
		return tts.ProviderErrorWorker
	case status == http.StatusTooManyRequests, status >= 500:
		return tts.ProviderErrorUnavailable
	default:
		return tts.ProviderErrorInput
	}
}

// fail builds the typed error the pipeline's fallback predicate reads. Kind
// Unavailable also unwraps to tts.ErrProviderUnavailable for NewTTSSet.
func (p *Provider) fail(op string, kind tts.ProviderErrorKind, err error) error {
	return &tts.ProviderError{Provider: config.OpenAISpeechProviderName, Operation: op, Kind: kind, Err: err}
}

// classify sorts a transport or body failure: the turn being cancelled is
// Canceled (no fallback), the per-request timeout is Worker (fallback), and
// malformed PCM is Malformed, and anything else is the server not answering
// (fallback). The stream's own
// write error on a cancelled turn is the turn context's error, so it lands in
// the first branch too.
func (p *Provider) classify(turnCtx, reqCtx context.Context, op string, err error) error {
	switch {
	case turnCtx.Err() != nil:
		return p.fail(op, tts.ProviderErrorCanceled, turnCtx.Err())
	case errors.Is(reqCtx.Err(), context.DeadlineExceeded):
		return p.fail(op, tts.ProviderErrorWorker, fmt.Errorf("timed out after %s: %w", p.timeout, err))
	case errors.Is(err, tts.ErrMalformedOutput):
		return p.fail(op, tts.ProviderErrorMalformed, err)
	default:
		return p.fail(op, tts.ProviderErrorUnavailable, err)
	}
}

// Phonon pads every utterance with about half a second of silence (measured
// 507-747 ms on 2026-10-23 against Kokoro's 41-85 ms). The pipeline requests
// sentence by sentence, so that pad would land between every two sentences
// as dead air. The trimmer holds back trailing sub-threshold samples, releases
// them the moment something audible follows (so a pause inside a sentence is
// kept), and drops whatever is still held at the end beyond tailKeep.
const (
	silenceThreshold = 0.002 // about -54 dBFS
	tailKeepSamples  = SampleRate * 80 / 1000
)

type frameWriter interface{ Write([]float32) error }

type tailTrimmer struct {
	out  frameWriter
	held []float32
	// owed is how much of tailKeep after the last audible sample has not been
	// written yet because the chunk ended first; silent chunks pay it down so
	// the kept tail is the same length however the body was chunked.
	owed int
	// wrote records that something audible reached out, so finish can tell a
	// trimmed pad from an answer that was silence all the way through.
	wrote bool
}

func newTailTrimmer(out frameWriter) *tailTrimmer { return &tailTrimmer{out: out} }

func (t *tailTrimmer) Write(samples []float32) error {
	if len(samples) == 0 {
		return nil
	}
	all := append(t.held, samples...)
	last := -1
	for i := len(all) - 1; i >= 0; i-- {
		if all[i] > silenceThreshold || all[i] < -silenceThreshold {
			last = i
			break
		}
	}
	if last < 0 {
		emit := min(t.owed, len(all))
		t.owed -= emit
		t.held = append([]float32(nil), all[emit:]...)
		if emit == 0 {
			return nil
		}
		return t.out.Write(all[:emit])
	}
	emit := min(last+1+tailKeepSamples, len(all))
	t.owed = tailKeepSamples - (emit - (last + 1))
	t.held = append([]float32(nil), all[emit:]...)
	t.wrote = true
	return t.out.Write(all[:emit])
}

// finish drops the held tail: everything after the last audible sample plus
// tailKeep has already been written. An answer that never crossed the
// threshold is played as up to tailKeep of silence instead: a stream with no
// samples at all is a failed sentence to the player, and a silent reply is
// not a failure.
func (t *tailTrimmer) finish() error {
	held := t.held
	t.held, t.owed = nil, 0
	if t.wrote || len(held) == 0 {
		return nil
	}
	return t.out.Write(held[:min(len(held), tailKeepSamples)])
}

// decodePCM16 converts a 16-bit little-endian mono body into float32 frames.
// An odd trailing byte is carried into the next read: dropping it would shift
// every later sample by one byte and turn speech into static. At EOF, an empty
// body or an incomplete final sample is malformed, even after valid frames.
func decodePCM16(r io.Reader, stream frameWriter) error {
	const frameSamples = 2048
	buf := make([]byte, frameSamples*2)
	var carry []byte
	decoded := false
	for {
		n, readErr := r.Read(buf)
		if n > 0 {
			data := buf[:n]
			if len(carry) > 0 {
				data = append(carry, data...)
				carry = nil
			}
			if len(data)%2 == 1 {
				carry = []byte{data[len(data)-1]}
				data = data[:len(data)-1]
			}
			if len(data) > 0 {
				decoded = true
				samples := make([]float32, len(data)/2)
				for i := range samples {
					samples[i] = float32(int16(binary.LittleEndian.Uint16(data[2*i:]))) / 32768
				}
				if err := stream.Write(samples); err != nil {
					return err
				}
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				if len(carry) != 0 {
					return fmt.Errorf("%w: speech PCM ends with an incomplete sample", tts.ErrMalformedOutput)
				}
				if !decoded {
					return fmt.Errorf("%w: speech response contains no PCM samples", tts.ErrMalformedOutput)
				}
				return nil
			}
			return readErr
		}
	}
}

// SplitForLimit cuts text into pieces of at most limit characters (runes, so a
// multibyte rune is never cut in half), preferring sentence ends, then
// whitespace, and only then a hard cut.
func SplitForLimit(text string, limit int) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	runes := []rune(text)
	if limit <= 0 || len(runes) <= limit {
		return []string{text}
	}
	var out []string
	for len(runes) > limit {
		window := runes[:limit]
		cut := -1
		for _, sep := range []string{". ", "! ", "? ", "\n", "; ", ", ", " "} {
			if i := lastRuneIndex(window, []rune(sep)); i > 0 {
				cut = i + len([]rune(sep))
				break
			}
		}
		if cut <= 0 {
			cut = limit
		}
		if piece := strings.TrimSpace(string(runes[:cut])); piece != "" {
			out = append(out, piece)
		}
		runes = []rune(strings.TrimSpace(string(runes[cut:])))
	}
	if len(runes) > 0 {
		out = append(out, string(runes))
	}
	return out
}

// lastRuneIndex is strings.LastIndex over rune slices.
func lastRuneIndex(hay, needle []rune) int {
	for i := len(hay) - len(needle); i >= 0; i-- {
		match := true
		for j := range needle {
			if hay[i+j] != needle[j] {
				match = false
				break
			}
		}
		if match {
			return i
		}
	}
	return -1
}

// Available reports whether the provider has been constructed and not closed.
func (p *Provider) Available() bool { return p.alive.Load() && (p.native == nil || p.native.Running()) }

// Delete closes the provider; later synthesis is refused.
func (p *Provider) Delete() {
	p.alive.Store(false)
	if p.native != nil {
		p.native.Close()
	}
}

// ProviderName is the configured provider id.
func (p *Provider) ProviderName() string { return config.OpenAISpeechProviderName }

// ModelTier is the model the server was asked for; it is what tells one
// measurement from another on this provider.
func (p *Provider) ModelTier() string { return p.model }

// ListVoices returns the catalog loadCatalog assembled. Filters apply only to
// voices that carry the attribute.
func (p *Provider) ListVoices(locale, gender string) []tts.Voice {
	return FilterVoices(p.voices, locale, gender)
}

// CatalogNotice explains a catalog that is only the configured voice when the
// user can do something about it; "" otherwise.
func (p *Provider) CatalogNotice() string { return p.notice }

// CatalogSource is "server" (GET /audio/voices), "ollama" (/api/show voices)
// or "configured" (nothing published; the configured voice stands alone).
func (p *Provider) CatalogSource() string { return p.catalogSource }

// FilterVoices applies the locale/gender filters the way Kokoro's catalog
// does, skipping the check for voices that do not state the attribute.
func FilterVoices(voices []tts.Voice, locale, gender string) []tts.Voice {
	out := make([]tts.Voice, 0, len(voices))
	for _, v := range voices {
		if locale != "" && v.Locale != "" && !strings.HasPrefix(v.Locale, locale) {
			continue
		}
		if gender != "" && v.Gender != "" && !strings.EqualFold(v.Gender, gender) {
			continue
		}
		out = append(out, v)
	}
	return out
}

// Capabilities describes the one static-voice mode the endpoint offers.
func (p *Provider) Capabilities() tts.ProviderCapabilities {
	return tts.ProviderCapabilities{
		Provider:             config.OpenAISpeechProviderName,
		Model:                p.model,
		ModelReady:           p.Available(),
		Modes:                []tts.VoiceModeCapability{{ID: tts.VoiceModeStatic, Voices: p.ListVoices("", "")}},
		SampleRates:          []int{SampleRate},
		SupportsPreview:      true,
		SupportsStreaming:    true,
		SupportsCancellation: true,
	}
}

// Status reports readiness for the Settings screen and doctor.
func (p *Provider) Status() tts.ProviderStatus {
	detail := p.detail
	if !p.Available() {
		detail = "provider is closed"
	}
	return tts.ProviderStatus{Provider: config.OpenAISpeechProviderName, Available: p.Available(), ModelReady: p.Available(), Detail: detail}
}

// Grace for first audio. Warm Phonon through Ollama answered in under 20 ms
// on an M4 Max, but Ollama unloads an idle model after a few minutes and the
// reload measured 2.4 s. The floor covers that reload; the per-character term
// covers a slower server.
const (
	minFirstAudioGrace = 5 * time.Second
	maxFirstAudioGrace = 20 * time.Second
	// rtfCeiling is the slowest synthesis this grace tolerates relative to
	// the audio it produces: one second of speech per second of synthesis.
	rtfCeiling = 1.0
)

// FirstAudioGrace is the stall budget for a segment of unstated length.
func (p *Provider) FirstAudioGrace() time.Duration {
	return p.FirstAudioGraceForChars(tts.TypicalSegmentChars)
}

// FirstAudioGraceForChars sizes the stall budget to the segment.
func (p *Provider) FirstAudioGraceForChars(chars int) time.Duration {
	if chars <= 0 {
		chars = tts.TypicalSegmentChars
	}
	expectedAudio := float64(chars) / tts.SpeechCharsPerSecond
	grace := minFirstAudioGrace + time.Duration(rtfCeiling*expectedAudio*float64(time.Second))
	if grace > maxFirstAudioGrace {
		return maxFirstAudioGrace
	}
	return grace
}

// errorMessage pulls error.message out of an OpenAI error envelope, falling
// back to the raw body or the given default.
func errorMessage(body []byte, fallback string) string {
	var env struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &env); err == nil && strings.TrimSpace(env.Error.Message) != "" {
		return strings.TrimSpace(env.Error.Message)
	}
	if s := strings.TrimSpace(string(body)); s != "" {
		return s
	}
	return fallback
}

// modelIDs reads the ids out of GET /models; nil when the body is not a list.
func modelIDs(body []byte) []string {
	var doc struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil
	}
	out := make([]string, 0, len(doc.Data))
	for _, m := range doc.Data {
		if m.ID != "" {
			out = append(out, m.ID)
		}
	}
	return out
}

// hasModel matches "phonon" against "phonon" and "phonon:latest".
func hasModel(ids []string, model string) bool {
	model = strings.ToLower(strings.TrimSpace(model))
	for _, id := range ids {
		id = strings.ToLower(id)
		if id == model || strings.HasPrefix(id, model+":") {
			return true
		}
	}
	return false
}

// voiceNames accepts the catalog shapes a speech server might publish: a bare
// array, {"voices": [...]}, or {"data": [...]}, with entries as strings or as
// objects carrying "name" or "id".
func voiceNames(body []byte) []string {
	var raw json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil
	}
	var list []json.RawMessage
	if err := json.Unmarshal(raw, &list); err != nil {
		var obj struct {
			Voices []json.RawMessage `json:"voices"`
			Data   []json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(raw, &obj); err != nil {
			return nil
		}
		list = obj.Voices
		if len(list) == 0 {
			list = obj.Data
		}
	}
	out := make([]string, 0, len(list))
	for _, item := range list {
		var s string
		if err := json.Unmarshal(item, &s); err == nil {
			out = append(out, strings.TrimSpace(s))
			continue
		}
		var o struct {
			Name string `json:"name"`
			ID   string `json:"id"`
		}
		if err := json.Unmarshal(item, &o); err == nil {
			if o.Name != "" {
				out = append(out, strings.TrimSpace(o.Name))
			} else if o.ID != "" {
				out = append(out, strings.TrimSpace(o.ID))
			}
		}
	}
	return out
}

// unwrapNet shortens a transport error to what the user can act on.
func unwrapNet(err error) error {
	var opErr *net.OpError
	if errors.As(err, &opErr) && opErr.Err != nil {
		return opErr.Err
	}
	return err
}
