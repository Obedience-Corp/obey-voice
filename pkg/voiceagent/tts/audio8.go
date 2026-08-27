package tts

import (
	"context"
	"encoding/binary"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/onnxrt"
)

const audio8ProviderName = "audio8"

// Audio8Tokenizer turns text into the model's token ids.
//
// This is a seam, not an oversight: the checkpoint ships a HuggingFace
// byte-level BPE (65k vocab, 64.7k merges, a Unicode-property pre-tokenizer
// sequence and 4621 added tokens) and there is no in-tree Go implementation.
// Everything else in this provider is exercised by the integration test using a
// fixture tokenizer; wiring a real one is a separate, explicit decision.
type Audio8Tokenizer interface {
	Encode(text string) ([]int64, error)
}

// Audio8TTS synthesizes speech with the Audio8 DualAR ONNX export.
//
// The graphs run on the ONNX Runtime that obey-voice already links via
// sherpa-onnx-go, so this provider adds no runtime dependency. Two behaviours
// are load-bearing for latency and are not optional:
//
//   - the slow graph is decode-only, so the prompt is fed one token at a time.
//     The system prompt plus the reference voice are identical on every turn, so
//     that state is primed once and cloned per utterance.
//   - codec decode runs concurrently with the AR loop, over non-overlapping
//     chunks. A large stream context re-decodes the same frames and is far
//     slower than no context at all.
type Audio8TTS struct {
	env               *onnxrt.Env
	slow, fast, codec *onnxrt.Session
	man               *audio8Manifest
	tok               Audio8Tokenizer

	refCodes [][]int64 // [numCodebooks][T]
	refText  string

	mu        sync.Mutex
	prefix    *slowState // primed state after the constant head
	prefixLen int

	chunkFrames int
	maxNew      int
	temperature float64
	topP        float64
	topK        int
}

// Audio8Options tunes the provider. Zero values take the measured defaults.
type Audio8Options struct {
	ModelDir     string
	Tokenizer    Audio8Tokenizer
	ARThreads    int
	CodecThreads int
	ChunkFrames  int
}

// NewAudio8TTS loads the export in modelDir.
func NewAudio8TTS(opts Audio8Options) (*Audio8TTS, error) {
	if opts.ModelDir == "" {
		return nil, fmt.Errorf("audio8: model dir is required")
	}
	man, err := loadAudio8Manifest(opts.ModelDir)
	if err != nil {
		return nil, err
	}
	// Measured on an M4 Max: the per-token AR graphs get slower with more
	// threads (sync overhead dominates) while the codec, one large op, wants
	// more. Hence per-graph thread counts rather than one global setting.
	arThreads := orDefault(opts.ARThreads, 5)
	codecThreads := orDefault(opts.CodecThreads, 10)

	env, err := onnxrt.NewEnv()
	if err != nil {
		return nil, err
	}
	a := &Audio8TTS{
		env: env, man: man, tok: opts.Tokenizer,
		chunkFrames: orDefault(opts.ChunkFrames, 12),
		maxNew:      1024, temperature: 0.7, topP: 0.9, topK: 50,
	}
	slowOut := []string{"logits", "hidden", "key_delta", "value_delta", "next_conv_states", "next_ssm_states"}
	if a.slow, err = onnxrt.NewSession(env, filepath.Join(opts.ModelDir, man.SlowDecodeModel), arThreads, 1, slowOut); err != nil {
		a.Delete()
		return nil, err
	}
	fastOut := []string{"logits"}
	for i := range man.NumFastLayers {
		fastOut = append(fastOut, fmt.Sprintf("key_delta_%d", i), fmt.Sprintf("value_delta_%d", i))
	}
	if a.fast, err = onnxrt.NewSession(env, filepath.Join(opts.ModelDir, man.FastModel), arThreads, 1, fastOut); err != nil {
		a.Delete()
		return nil, err
	}
	codecPath := filepath.Join(opts.ModelDir, man.CodecModels[man.DefaultCodecPrec])
	if a.codec, err = onnxrt.NewSession(env, codecPath, codecThreads, codecThreads/2, []string{"audio"}); err != nil {
		a.Delete()
		return nil, err
	}
	if a.refCodes, err = loadNPYInt64Rows(filepath.Join(opts.ModelDir, man.ReferenceCodes), man.NumCodebooks); err != nil {
		a.Delete()
		return nil, err
	}
	a.refText = man.ReferenceText
	return a, nil
}

func orDefault(v, def int) int {
	if v > 0 {
		return v
	}
	return def
}

// Delete releases the sessions and environment.
func (a *Audio8TTS) Delete() {
	for _, s := range []*onnxrt.Session{a.slow, a.fast, a.codec} {
		if s != nil {
			s.Close()
		}
	}
	a.slow, a.fast, a.codec = nil, nil, nil
	if a.env != nil {
		a.env.Close()
		a.env = nil
	}
}

// Available reports whether the provider can synthesize.
func (a *Audio8TTS) Available() bool { return a != nil && a.slow != nil && a.tok != nil }

// ListVoices returns the registered reference voices. Only the packaged
// reference is available until voice registration is wired.
func (a *Audio8TTS) ListVoices(locale, gender string) []Voice {
	return staticAudio8Voices(locale, gender)
}

func staticAudio8Voices(locale, gender string) []Voice {
	v := Voice{Name: "reference", FriendlyName: "Audio8 packaged reference", Locale: "en-us"}
	if locale != "" && !strings.HasPrefix(v.Locale, locale) {
		return nil
	}
	if gender != "" {
		return nil
	}
	return []Voice{v}
}

var audio8SpeakerRE = regexp.MustCompile(`<\|speaker:\d+\|>`)

// buildPrompt lays out [1, numCodebooks+1, N]: row 0 carries text ids and the
// reference semantic ids, rows 1..n carry the reference codebooks.
func (a *Audio8TTS) buildPrompt(target string) (rows [][]int64, constHead int, err error) {
	refText := a.refText
	if !audio8SpeakerRE.MatchString(refText) {
		refText = "<|speaker:0|>" + refText
	}
	prefixParts := []string{
		"<|im_start|>system\n",
		"convert the provided text to speech reference to the following:\n\nText:\n",
		refText,
		"\n\nSpeech:\n",
	}
	suffixParts := []string{
		"<|im_end|>\n", "<|im_start|>user\n", target, "<|im_end|>\n",
		"<|im_start|>assistant\n<|voice|>",
	}
	encodeAll := func(parts []string) ([]int64, error) {
		var out []int64
		for _, p := range parts {
			ids, err := a.tok.Encode(p)
			if err != nil {
				return nil, err
			}
			out = append(out, ids...)
		}
		return out, nil
	}
	prefix, err := encodeAll(prefixParts)
	if err != nil {
		return nil, 0, err
	}
	suffix, err := encodeAll(suffixParts)
	if err != nil {
		return nil, 0, err
	}
	refLen := len(a.refCodes[0])
	row0 := make([]int64, 0, len(prefix)+refLen+len(suffix))
	row0 = append(row0, prefix...)
	for _, c := range a.refCodes[0] {
		row0 = append(row0, c+int64(a.man.SemanticBeginID))
	}
	row0 = append(row0, suffix...)

	rows = make([][]int64, a.man.NumCodebooks+1)
	rows[0] = row0
	for cb := 1; cb <= a.man.NumCodebooks; cb++ {
		r := make([]int64, len(row0))
		copy(r[len(prefix):len(prefix)+refLen], a.refCodes[cb-1])
		rows[cb] = r
	}
	return rows, len(prefix) + refLen, nil
}

// primePrefix runs the constant head once and caches the resulting state. The
// system prompt and reference voice are identical every turn, so this is the
// difference between ~1.1 s and ~110 ms of prefill per utterance.
func (a *Audio8TTS) primePrefix() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.prefix != nil {
		return nil
	}
	rows, constHead, err := a.buildPrompt("priming")
	if err != nil {
		return err
	}
	st := newSlowState(a.man)
	col := make([]int64, a.man.NumCodebooks+1)
	for pos := range constHead {
		for r := range rows {
			col[r] = rows[r][pos]
		}
		if _, _, err := a.slowStep(st, col, pos); err != nil {
			return err
		}
	}
	a.prefix, a.prefixLen = st, constHead
	return nil
}

// loadNPYInt64Rows reads a 2-D little-endian int64 .npy into rows.
func loadNPYInt64Rows(path string, wantRows int) ([][]int64, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	if len(raw) < 10 || string(raw[:6]) != "\x93NUMPY" {
		return nil, fmt.Errorf("%s: not a .npy file", path)
	}
	headerLen := int(binary.LittleEndian.Uint16(raw[8:10]))
	if len(raw) < 10+headerLen {
		return nil, fmt.Errorf("%s: truncated header", path)
	}
	header := string(raw[10 : 10+headerLen])
	if !strings.Contains(header, "'<i8'") {
		return nil, fmt.Errorf("%s: want dtype <i8, header %q", path, header)
	}
	if strings.Contains(header, "'fortran_order': True") {
		return nil, fmt.Errorf("%s: fortran order not supported", path)
	}
	shape, err := parseNPYShape(header)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if len(shape) != 2 || shape[0] != wantRows {
		return nil, fmt.Errorf("%s: want shape [%d, T], got %v", path, wantRows, shape)
	}
	body := raw[10+headerLen:]
	if len(body) < shape[0]*shape[1]*8 {
		return nil, fmt.Errorf("%s: truncated data", path)
	}
	rows := make([][]int64, shape[0])
	for r := range rows {
		rows[r] = make([]int64, shape[1])
		for c := range rows[r] {
			off := (r*shape[1] + c) * 8
			rows[r][c] = int64(binary.LittleEndian.Uint64(body[off : off+8]))
		}
	}
	return rows, nil
}

var npyShapeRE = regexp.MustCompile(`'shape':\s*\(([^)]*)\)`)

func parseNPYShape(header string) ([]int, error) {
	m := npyShapeRE.FindStringSubmatch(header)
	if m == nil {
		return nil, fmt.Errorf("no shape in header")
	}
	var dims []int
	for _, f := range strings.Split(m[1], ",") {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		n, err := strconv.Atoi(f)
		if err != nil {
			return nil, fmt.Errorf("bad shape entry %q", f)
		}
		dims = append(dims, n)
	}
	return dims, nil
}

// Synthesize streams synthesized PCM at the model's native rate. The player
// resamples to the device rate, so no resampling happens here.
func (a *Audio8TTS) Synthesize(ctx context.Context, text string) (*audio.PCMStream, error) {
	res, err := a.SynthesizeRequest(ctx, SynthesisRequest{Text: text})
	if err != nil {
		return nil, err
	}
	return res.Stream, nil
}

// SynthesizeRequest runs the AR loop and pipelines codec decode against it.
func (a *Audio8TTS) SynthesizeRequest(ctx context.Context, req SynthesisRequest) (SynthesisResult, error) {
	if !a.Available() {
		return SynthesisResult{}, fmt.Errorf("audio8: provider not available (no tokenizer configured)")
	}
	if req.SampleRate != 0 && req.SampleRate != a.man.SampleRate {
		return SynthesisResult{}, fmt.Errorf("audio8 cannot resample to %d Hz (native %d Hz)", req.SampleRate, a.man.SampleRate)
	}
	if err := a.primePrefix(); err != nil {
		return SynthesisResult{}, err
	}
	stream := audio.NewPCMStream(ctx)

	go func() {
		defer func() {
			if r := recover(); r != nil {
				stream.CloseWithError(fmt.Errorf("audio8 panic: %v", r))
			}
		}()
		if err := stream.SetSampleRate(a.man.SampleRate); err != nil {
			stream.CloseWithError(err)
			return
		}
		if strings.TrimSpace(req.Text) == "" {
			stream.Close()
			return
		}
		if err := a.generate(ctx, req.Text, stream); err != nil {
			stream.CloseWithError(err)
			return
		}
		stream.Close()
	}()

	return SynthesisResult{
		Stream: stream, SampleRate: a.man.SampleRate,
		Provider: audio8ProviderName, Model: "audio8-tts-0.1b-onnx-int8",
		Voice: "reference",
	}, nil
}

// generate runs the AR loop, handing completed chunks to a codec goroutine so
// decode overlaps generation.
func (a *Audio8TTS) generate(ctx context.Context, text string, stream *audio.PCMStream) error {
	// One synthesis at a time: the sessions and the primed state are shared.
	a.mu.Lock()
	defer a.mu.Unlock()

	rows, constHead, err := a.buildPrompt(text)
	if err != nil {
		return err
	}
	st := a.prefix.clone()
	col := make([]int64, a.man.NumCodebooks+1)
	n := len(rows[0])
	var logits, hidden []float32
	for pos := constHead; pos < n; pos++ {
		for r := range rows {
			col[r] = rows[r][pos]
		}
		if logits, hidden, err = a.slowStep(st, col, pos); err != nil {
			return err
		}
	}

	type chunk struct{ frames [][]int64 }
	work := make(chan chunk, 4)
	errc := make(chan error, 1)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for c := range work {
			pcm, err := a.decodeFrames(c.frames)
			if err != nil {
				select {
				case errc <- err:
				default:
				}
				return
			}
			if err := stream.Write(pcm); err != nil {
				select {
				case errc <- err:
				default:
				}
				return
			}
		}
	}()
	flush := func(frames [][]int64) bool {
		select {
		case work <- chunk{frames: frames}:
			return true
		case err := <-errc:
			errc <- err
			return false
		}
	}

	rng := rand.New(rand.NewSource(42))
	var pending [][]int64
	var prev []int
	for step := range a.maxNew {
		if ctx.Err() != nil {
			close(work)
			wg.Wait()
			return ctx.Err()
		}
		sem := a.sampleSemantic(logits, prev, rng)
		if sem == a.man.ImEndID {
			break
		}
		prev = append(prev, sem)
		if len(prev) > 10 {
			prev = prev[len(prev)-10:]
		}
		frame, err := a.sampleFrame(hidden, sem, rng)
		if err != nil {
			close(work)
			wg.Wait()
			return err
		}
		pending = append(pending, frame)
		if len(pending) == a.chunkFrames {
			if !flush(pending) {
				break
			}
			pending = nil
		}
		col[0] = int64(sem)
		for i, v := range frame {
			col[i+1] = v
		}
		if logits, hidden, err = a.slowStep(st, col, n+step); err != nil {
			close(work)
			wg.Wait()
			return err
		}
	}
	if len(pending) > 0 {
		flush(pending)
	}
	close(work)
	wg.Wait()
	select {
	case err := <-errc:
		return err
	default:
		return nil
	}
}

// sampleSemantic picks the next semantic token under the relative logit layout.
func (a *Audio8TTS) sampleSemantic(logits []float32, prev []int, rng *rand.Rand) int {
	pick := func(i int) int {
		if i == len(logits)-1 {
			return a.man.ImEndID
		}
		return a.man.SemanticBeginID + i
	}
	normal := pick(sampleLogits(logits, a.temperature, a.topP, a.topK, rng))
	high := pick(sampleLogits(logits, 1.0, 0.9, a.topK, rng))
	if normal != a.man.ImEndID {
		for _, p := range prev {
			if p == normal {
				return high
			}
		}
	}
	return normal
}

// sampleFrame expands one semantic token into a full codebook frame.
func (a *Audio8TTS) sampleFrame(hidden []float32, sem int, rng *rand.Rand) ([]int64, error) {
	fs := newFastState(a.man)
	if _, err := a.fastStep(fs, hidden, 0, true, 0); err != nil {
		return nil, err
	}
	tok := sem - a.man.SemanticBeginID
	if tok < 0 {
		tok = 0
	}
	if tok > a.man.CodebookSize-1 {
		tok = a.man.CodebookSize - 1
	}
	frame := make([]int64, a.man.NumCodebooks)
	frame[0] = int64(tok)
	for cb := 1; cb < a.man.NumCodebooks; cb++ {
		fl, err := a.fastStep(fs, hidden, tok, false, cb)
		if err != nil {
			return nil, err
		}
		tok = sampleLogits(fl, a.temperature, a.topP, a.topK, rng)
		frame[cb] = int64(tok)
	}
	return frame, nil
}

// decodeFrames runs the codec over one chunk. Chunks are non-overlapping: the
// decoder is linear in frame count with no fixed cost, so re-decoding a context
// window multiplies the work without buying anything back.
func (a *Audio8TTS) decodeFrames(frames [][]int64) ([]float32, error) {
	nb, nf := a.man.NumCodebooks, len(frames)
	flat := make([]int64, nb*nf)
	for f, frame := range frames {
		for cb := range nb {
			flat[cb*nf+f] = frame[cb]
		}
	}
	out, err := a.codec.Run(map[string]*onnxrt.Tensor{
		"codes": onnxrt.I64Tensor(flat, 1, int64(nb), int64(nf)),
	})
	if err != nil {
		return nil, fmt.Errorf("codec decode: %w", err)
	}
	return out[0].Data, nil
}

// Capabilities reports what this provider supports.
func (a *Audio8TTS) Capabilities() ProviderCapabilities {
	return ProviderCapabilities{
		Provider:          audio8ProviderName,
		Model:             "audio8-tts-0.1b-onnx-int8",
		ModelReady:        a.Available(),
		SampleRates:       []int{a.man.SampleRate},
		SupportsStreaming: true,
		// Reference audio is the model's native conditioning path, but voice
		// registration (encoding a new reference) is not wired yet.
		SupportsReferenceAudio: false,
	}
}

var _ Provider = (*Audio8TTS)(nil)
var _ RequestProvider = (*Audio8TTS)(nil)

func audio8ModelDir(cfg *config.Config) string {
	return filepath.Join(cfg.ModelsDir, "audio8-tts")
}

// errAudio8NoTokenizer is returned until a text tokenizer is wired. The model
// ships a HuggingFace byte-level BPE and there is no in-tree Go implementation;
// see Audio8Tokenizer.
var errAudio8NoTokenizer = fmt.Errorf(
	"audio8: no tokenizer configured - the export needs a HuggingFace byte-level BPE and obey-voice has no Go implementation yet")
