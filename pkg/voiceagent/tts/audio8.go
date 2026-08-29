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
// The checkpoint ships a HuggingFace byte-level BPE (65k vocab, 64.7k merges, a
// Unicode-property pre-tokenizer sequence and 4621 added tokens); tts/hfbpe
// implements it and NewAudio8Tokenizer loads it from the model dir. The seam
// stays so the integration test can replay captured ids without the export,
// and so a future checkpoint can bring its own frontend.
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

	// modelDir is the export; modelsDir is the tree the registered-voice store
	// hangs off, which is deliberately outside the export - that is usually a
	// symlink tree into a read-only cache.
	modelDir     string
	modelsDir    string
	defaultVoice string

	refCodes [][]int64 // packaged reference, [numCodebooks][T]
	refText  string

	// mu guards the sessions, the resolved-voice cache and the primed prefixes.
	// One synthesis runs at a time whatever the voice, because all three graphs
	// are shared; the caches are per voice.
	mu     sync.Mutex
	voices map[string]*audio8Voice
	// prefixes is bounded; prefixLRU holds its keys, least recently used first.
	// The resolved-voice cache beside it is not bounded on purpose: a voice is
	// its codes, tens of kilobytes, where a primed prefix is tens of megabytes.
	prefixes  map[string]*audio8Prefix
	prefixLRU []string

	chunkFrames int
	maxNew      int
	temperature float64
	topP        float64
	topK        int
}

// Audio8Options tunes the provider. Zero values take the measured defaults.
type Audio8Options struct {
	ModelDir string
	// ModelsDir is the models tree holding the registered-voice store
	// (config.Audio8VoicesDir under it). Empty means the export's packaged
	// reference is the only voice, which is what a caller with no models dir
	// (a test, a probe) should get.
	ModelsDir string
	// Voice is the voice to speak in when a request does not name one. Empty
	// selects the packaged reference.
	Voice        string
	Tokenizer    Audio8Tokenizer
	ARThreads    int
	CodecThreads int
	ChunkFrames  int
}

// audio8Voice is a resolved reference: the codes the model conditions on and
// the transcript of the audio they came from.
type audio8Voice struct {
	name       string
	codes      [][]int64
	transcript string
}

// audio8Prefix is the primed slow-graph state after a voice's constant head.
type audio8Prefix struct {
	state  *slowState
	length int
}

// bytes is the entry's resident size, from the state's own slices.
func (p *audio8Prefix) bytes() int {
	if p == nil || p.state == nil {
		return 0
	}
	s := p.state
	return 4 * (len(s.cacheKeys) + len(s.cacheValues) + len(s.convStates) + len(s.ssmStates))
}

// audio8PrefixCacheSize bounds how many voices stay primed at once.
//
// One entry is the slow graph's entire recurrent state, and it is large: with
// the shipped export's dimensions (24 layers, 2 local heads, max_seq_len 2048,
// head dim 64) each KV cache is 24*2*2048*64 float32 = 25,165,824 bytes, and
// there are two of them, plus 4,718,592 of SSM state and 344,064 of conv state.
// 55,394,304 bytes - 52.8 MiB - per primed voice, asserted against the
// allocator by TestAudio8PrefixEntrySizeMatchesTheDocumentedBound. Now that a
// user can register as many voices as they like, an unbounded cache is an
// unbounded leak: ten auditioned voices would pin half a gigabyte for the life
// of the process.
//
// Three is the working set that actually occurs: the voice being spoken, the
// one just switched away from, and one more for a persona that alternates.
// Past that the least recently used entry is dropped and re-primed if it comes
// back, at the ~1.0 s recorded in
// docs/benchmarks/A1-audio8-tts-onnx-2026-08-27.json.
const audio8PrefixCacheSize = 3

// NewAudio8TTS loads the export in modelDir.
func NewAudio8TTS(opts Audio8Options) (*Audio8TTS, error) {
	if opts.ModelDir == "" {
		return nil, fmt.Errorf("audio8: model dir is required")
	}
	if opts.Tokenizer == nil {
		// Refuse before touching the manifest or the ~1.6 GB of graphs: a
		// tokenizer-less provider can never synthesize, so paying the model
		// load only to reject it afterwards would waste seconds and memory.
		return nil, errAudio8NoTokenizer
	}
	man, err := loadAudio8Manifest(opts.ModelDir)
	if err != nil {
		return nil, err
	}
	// Opening the store is where residue from an interrupted registration gets
	// noticed; nothing else ever looks at it. Best effort by design - a store
	// that cannot be tidied still speaks.
	if dir := strings.TrimSpace(opts.ModelsDir); dir != "" {
		SweepAudio8VoiceStore(dir)
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
		modelDir:     opts.ModelDir,
		modelsDir:    strings.TrimSpace(opts.ModelsDir),
		defaultVoice: audio8VoiceOrDefault(opts.Voice),
		voices:       map[string]*audio8Voice{},
		prefixes:     map[string]*audio8Prefix{},
		chunkFrames:  orDefault(opts.ChunkFrames, 12),
		maxNew:       1024, temperature: 0.7, topP: 0.9, topK: 50,
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

// ListVoices returns the packaged reference plus every registered voice.
//
// A store that cannot be read falls back to the packaged voice rather than
// failing: the provider can still speak, and `voices registered` is the surface
// that reports why a voice is missing.
func (a *Audio8TTS) ListVoices(locale, gender string) []Voice {
	voices, err := audio8VoiceCatalog(a.modelsDir, locale, gender)
	if err != nil {
		return staticAudio8Voices(locale, gender)
	}
	return voices
}

func staticAudio8Voices(locale, gender string) []Voice {
	v := Voice{Name: config.Audio8PackagedVoice, FriendlyName: "Audio8 packaged reference", Locale: "en-us"}
	if locale != "" && !strings.HasPrefix(v.Locale, locale) {
		return nil
	}
	if gender != "" {
		return nil
	}
	return []Voice{v}
}

// audio8VoiceOrDefault normalizes a requested voice name; empty means the
// packaged reference.
func audio8VoiceOrDefault(name string) string {
	if trimmed := strings.TrimSpace(name); trimmed != "" {
		return trimmed
	}
	return config.Audio8PackagedVoice
}

// resolveVoice returns the codes and transcript for a requested voice, loading
// and caching a registered one on first use. Empty falls back to the provider's
// configured voice, then to the packaged reference.
func (a *Audio8TTS) resolveVoice(name string) (*audio8Voice, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		name = a.defaultVoice
	}
	name = audio8VoiceOrDefault(name)

	a.mu.Lock()
	defer a.mu.Unlock()
	if v := a.voices[name]; v != nil {
		return v, nil
	}
	if name == config.Audio8PackagedVoice {
		v := &audio8Voice{name: name, codes: a.refCodes, transcript: a.refText}
		a.voices[name] = v
		return v, nil
	}
	if a.modelsDir == "" {
		return nil, fmt.Errorf("audio8: no voice store configured, so %q cannot be resolved (only %q is available)",
			name, config.Audio8PackagedVoice)
	}
	codes, meta, err := loadAudio8VoiceCodes(a.modelsDir, name, a.man.ModelFingerprint, a.man.NumCodebooks)
	if err != nil {
		return nil, err
	}
	v := &audio8Voice{name: name, codes: codes, transcript: meta.Transcript}
	a.voices[name] = v
	return v, nil
}

var audio8SpeakerRE = regexp.MustCompile(`<\|speaker:\d+\|>`)

// buildPrompt lays out [1, numCodebooks+1, N]: row 0 carries text ids and the
// reference semantic ids, rows 1..n carry the reference codebooks.
func (a *Audio8TTS) buildPrompt(voice *audio8Voice, target string) (rows [][]int64, constHead int, err error) {
	refText := voice.transcript
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
	refLen := len(voice.codes[0])
	row0 := make([]int64, 0, len(prefix)+refLen+len(suffix))
	row0 = append(row0, prefix...)
	for _, c := range voice.codes[0] {
		row0 = append(row0, c+int64(a.man.SemanticBeginID))
	}
	row0 = append(row0, suffix...)

	rows = make([][]int64, a.man.NumCodebooks+1)
	rows[0] = row0
	for cb := 1; cb <= a.man.NumCodebooks; cb++ {
		r := make([]int64, len(row0))
		copy(r[len(prefix):len(prefix)+refLen], voice.codes[cb-1])
		rows[cb] = r
	}
	return rows, len(prefix) + refLen, nil
}

// prefixFor runs one voice's constant head once and caches the resulting state.
// The system prompt and the reference voice are identical on every turn in that
// voice, so this is the difference between ~1.1 s and ~110 ms of prefill per
// utterance (docs/benchmarks/A1-audio8-tts-onnx-2026-08-27.json).
//
// The cache is per voice, and switching voices pays the prime again: the
// reference frames are part of the primed state, so one cache entry cannot
// serve two voices. It holds at most audio8PrefixCacheSize of them - see there
// for why that number and what one costs.
func (a *Audio8TTS) prefixFor(voice *audio8Voice) (*audio8Prefix, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if p := a.lookupPrefixLocked(voice.name); p != nil {
		return p, nil
	}
	rows, constHead, err := a.buildPrompt(voice, "priming")
	if err != nil {
		return nil, err
	}
	st := newSlowState(a.man)
	col := make([]int64, a.man.NumCodebooks+1)
	for pos := range constHead {
		for r := range rows {
			col[r] = rows[r][pos]
		}
		if _, _, err := a.slowStep(st, col, pos); err != nil {
			return nil, err
		}
	}
	p := &audio8Prefix{state: st, length: constHead}
	a.insertPrefixLocked(voice.name, p)
	return p, nil
}

// lookupPrefixLocked returns the primed prefix for name, marking it as most
// recently used. a.mu must be held.
func (a *Audio8TTS) lookupPrefixLocked(name string) *audio8Prefix {
	p := a.prefixes[name]
	if p != nil {
		a.touchPrefixLocked(name)
	}
	return p
}

// insertPrefixLocked stores a freshly primed prefix and evicts down to the
// bound. a.mu must be held.
//
// Eviction drops the map's reference, not the memory: a caller that took a
// prefix from prefixFor and has not reached generate yet still holds a valid
// pointer, and generate clones the state it is given. An evicted voice is
// simply re-primed the next time it is asked for.
func (a *Audio8TTS) insertPrefixLocked(name string, p *audio8Prefix) {
	a.prefixes[name] = p
	a.touchPrefixLocked(name)
	for len(a.prefixLRU) > audio8PrefixCacheSize {
		evicted := a.prefixLRU[0]
		a.prefixLRU = a.prefixLRU[1:]
		delete(a.prefixes, evicted)
	}
}

// touchPrefixLocked moves name to the most-recently-used end. a.mu must be held.
func (a *Audio8TTS) touchPrefixLocked(name string) {
	for i, n := range a.prefixLRU {
		if n == name {
			a.prefixLRU = append(a.prefixLRU[:i], a.prefixLRU[i+1:]...)
			break
		}
	}
	a.prefixLRU = append(a.prefixLRU, name)
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

// writeNPYInt64Rows writes rows as a 2-D little-endian int64 .npy, the shape
// loadNPYInt64Rows reads back and the shape the export's own
// reference_codes.npy uses. A registered voice is stored in the packaged
// voice's format so one loader serves both.
func writeNPYInt64Rows(path string, rows [][]int64) error {
	if len(rows) == 0 || len(rows[0]) == 0 {
		return fmt.Errorf("write %s: refusing to write empty codes", path)
	}
	cols := len(rows[0])
	for r, row := range rows {
		if len(row) != cols {
			return fmt.Errorf("write %s: row %d has %d columns, want %d", path, r, len(row), cols)
		}
	}
	header := fmt.Sprintf("{'descr': '<i8', 'fortran_order': False, 'shape': (%d, %d), }", len(rows), cols)
	// numpy pads the header with spaces so the data starts on a 64-byte
	// boundary, and the header always ends with a newline.
	const preamble = 10 // magic(6) + version(2) + header length(2)
	pad := 64 - (preamble+len(header)+1)%64
	if pad == 64 {
		pad = 0
	}
	header += strings.Repeat(" ", pad) + "\n"

	buf := make([]byte, 0, preamble+len(header)+len(rows)*cols*8)
	buf = append(buf, "\x93NUMPY"...)
	buf = append(buf, 1, 0)
	buf = binary.LittleEndian.AppendUint16(buf, uint16(len(header)))
	buf = append(buf, header...)
	for _, row := range rows {
		for _, v := range row {
			buf = binary.LittleEndian.AppendUint64(buf, uint64(v))
		}
	}
	return os.WriteFile(path, buf, 0o600)
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
	voice, err := a.resolveVoice(req.Voice)
	if err != nil {
		return SynthesisResult{}, err
	}
	// Priming is synchronous so an unusable voice fails the call rather than
	// the stream, and so the caller's first read is not the one that waits a
	// second for a cold prefix.
	prefix, err := a.prefixFor(voice)
	if err != nil {
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
		if err := a.generate(ctx, voice, prefix, req.Text, stream); err != nil {
			stream.CloseWithError(err)
			return
		}
		stream.Close()
	}()

	return SynthesisResult{
		Stream: stream, SampleRate: a.man.SampleRate,
		Provider: audio8ProviderName, Model: "audio8-tts-0.1b-onnx-int8",
		Voice: voice.name,
	}, nil
}

// generate runs the AR loop, handing completed chunks to a codec goroutine so
// decode overlaps generation.
func (a *Audio8TTS) generate(ctx context.Context, voice *audio8Voice, prefix *audio8Prefix, text string, stream *audio.PCMStream) error {
	// One synthesis at a time: the sessions are shared across voices. The
	// prefix is only ever read here - prefixFor never mutates a cached entry -
	// so cloning it outside the lock would be safe too; taking the lock first
	// keeps the graphs single-threaded, which is what actually matters.
	a.mu.Lock()
	defer a.mu.Unlock()

	rows, constHead, err := a.buildPrompt(voice, text)
	if err != nil {
		return err
	}
	if constHead != prefix.length {
		return fmt.Errorf("audio8: voice %q primed %d tokens but this prompt has a %d token head",
			voice.name, prefix.length, constHead)
	}
	st := prefix.state.clone()
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
		// Reference audio is the model's native conditioning path. It is
		// supported only when the export carries the registration package -
		// the encoder is an optional part of the snapshot, and without it
		// there is no way to turn audio into a voice.
		SupportsReferenceAudio: Audio8RegistrationInstalled(a.modelDir),
	}
}

var _ Provider = (*Audio8TTS)(nil)
var _ RequestProvider = (*Audio8TTS)(nil)

func audio8ModelDir(cfg *config.Config) string {
	// ModelsDirFrom, not raw cfg.ModelsDir: kokoro/qwen resolve through it, and
	// an empty ModelsDir must not make this row disagree with theirs.
	return filepath.Join(config.ModelsDirFrom(cfg), config.Audio8Subdir)
}

// errAudio8NoTokenizer rejects a provider that could never synthesize. The
// factory builds one with NewAudio8Tokenizer; a caller constructing Audio8TTS
// directly has to supply its own.
var errAudio8NoTokenizer = fmt.Errorf(
	"audio8: no tokenizer configured - pass Audio8Options.Tokenizer (see NewAudio8Tokenizer)")
