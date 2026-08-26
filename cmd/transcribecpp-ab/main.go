// Command transcribecpp-ab is the WI-26ae84 spike-1 driver (E0: measure only,
// no factory code). transcribe.cpp has no stt_provider, so `obey-voice
// benchmark` cannot run it; this tool shells the pinned transcribe-cli over
// the same fixtures the control ran and emits benchmarkResult-shaped records
// that cmd/stt-ab can aggregate next to the control's.
//
// Two modes:
//
//	transcribecpp-ab -synth-only -fixtures scripts/stt-ab-fixtures.txt -fixture-dir <dir>
//	transcribecpp-ab -fixtures ... -fixture-dir <dir> -cli <transcribe-cli> \
//	  -model <gguf> -backend metal -label transcribecpp-metal -iterations 3 -out records.json
//
// Latency basis: metrics.STTFinalElapsed for these records is the CLI's
// per-file compute time (mel+encode+decode, model loaded once per batch).
// That is comparable to a control run with --fixture-realtime=false, not to
// the realtime harness numbers. Non-speech fixtures (generated silence and
// noise) are recorded under "<label>-nonspeech" with an empty expected text so
// the aggregator's accuracy/latency stats never include them; they exist only
// for the hallucination check.
//
// Every fixture wav carries a <wav>.recipe sidecar holding a hash of the text
// and synth settings that produced it. A missing or stale sidecar re-synthesizes
// the wav, so an edited fixture list can never be scored against old audio.
package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
)

const (
	fixtureRate      = 16000
	trailingSilenceS = 1.5
	synthVoice       = "af_heart"
	synthSpeed       = "0.95"
	noiseSeed        = 0x26ae84
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "transcribecpp-ab:", err)
		os.Exit(1)
	}
}

type options struct {
	fixtures   string
	fixtureDir string
	synthOnly  bool
	cli        string
	model      string
	backend    string
	label      string
	iterations int
	out        string
}

func run() error {
	var o options
	flag.StringVar(&o.fixtures, "fixtures", "scripts/stt-ab-fixtures.txt", "fixture text list, one utterance per line")
	flag.StringVar(&o.fixtureDir, "fixture-dir", "", "directory for synthesized 16 kHz wavs (required)")
	flag.BoolVar(&o.synthOnly, "synth-only", false, "synthesize missing fixtures and exit")
	flag.StringVar(&o.cli, "cli", "", "path to transcribe-cli")
	flag.StringVar(&o.model, "model", "", "path to the GGUF model")
	flag.StringVar(&o.backend, "backend", "cpu", "transcribe-cli --backend value (explicit, never auto: a silent fallback is not a result)")
	flag.StringVar(&o.label, "label", "", "provider label for the records, e.g. transcribecpp-metal")
	flag.IntVar(&o.iterations, "iterations", 3, "batch passes over the fixture set")
	flag.StringVar(&o.out, "out", "", "output path for the records array; stdout when empty")
	flag.Parse()

	if o.fixtureDir == "" {
		return fmt.Errorf("-fixture-dir is required")
	}
	texts, err := readFixtureTexts(o.fixtures)
	if err != nil {
		return err
	}
	speech, err := ensureFixtures(texts, o.fixtureDir)
	if err != nil {
		return err
	}
	nonspeech, err := ensureNonSpeech(o.fixtureDir)
	if err != nil {
		return err
	}
	if o.synthOnly {
		fmt.Fprintf(os.Stderr, "fixtures ready: %d speech + %d non-speech in %s\n", len(speech), len(nonspeech), o.fixtureDir)
		return nil
	}

	if o.cli == "" || o.model == "" || o.label == "" {
		return fmt.Errorf("-cli, -model and -label are required unless -synth-only")
	}

	var records []record
	for iter := 1; iter <= o.iterations; iter++ {
		batch, err := runBatch(o, append(append([]fixture{}, speech...), nonspeech...))
		if err != nil {
			return fmt.Errorf("iteration %d: %w", iter, err)
		}
		for _, b := range batch {
			records = append(records, makeRecord(o.label, iter, b))
		}
	}

	data, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if o.out == "" {
		_, err = os.Stdout.Write(data)
		return err
	}
	return os.WriteFile(o.out, data, 0o644)
}

type fixture struct {
	wav      string
	expected string
}

type record struct {
	Mode            string  `json:"mode"`
	Provider        string  `json:"provider"`
	Iteration       int     `json:"iteration"`
	Fixture         string  `json:"fixture"`
	ElapsedNS       int64   `json:"elapsed"`
	Metrics         metrics `json:"metrics"`
	Transcript      string  `json:"transcript,omitempty"`
	Expected        string  `json:"expected,omitempty"`
	TranscriptScore float64 `json:"transcript_score,omitempty"`
}

// metrics carries only the key cmd/stt-ab reads. The harness serializes the
// full events.TurnMetrics; these records are CLI runs, so the other stages do
// not exist.
type metrics struct {
	STTFinalElapsed int64 `json:"STTFinalElapsed"`
}

// makeRecord shapes one batch result. A fixture with no expected text is
// non-speech: it lands under a "-nonspeech" provider with no expected and no
// score, so the aggregator's accuracy and latency stats never see it.
func makeRecord(label string, iter int, b batchResult) record {
	r := record{
		Mode:       "stt",
		Provider:   label,
		Iteration:  iter,
		Fixture:    b.fix.wav,
		ElapsedNS:  b.computeNS,
		Metrics:    metrics{STTFinalElapsed: b.computeNS},
		Transcript: b.text,
	}
	if b.fix.expected == "" {
		r.Provider = label + "-nonspeech"
		return r
	}
	r.Expected = b.fix.expected
	r.TranscriptScore = transcriptScore(b.fix.expected, b.text)
	return r
}

func readFixtureTexts(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var texts []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		texts = append(texts, line)
	}
	return texts, sc.Err()
}

// speechRecipe is everything that decides the bytes of a speech fixture. Its
// hash is written beside the wav so a changed text or synth setting forces a
// re-synthesis instead of silently scoring last week's audio.
func speechRecipe(text string) string {
	return fmt.Sprintf("v1\nkind=speech\nengine=golden-tts\nvoice=%s\nspeed=%s\nrate=%d\nsilence=%g\ntext=%s\n",
		synthVoice, synthSpeed, fixtureRate, trailingSilenceS, text)
}

func nonSpeechRecipe(kind string, samples int) string {
	return fmt.Sprintf("v1\nkind=%s\nrate=%d\nsamples=%d\nseed=%#x\n", kind, fixtureRate, samples, noiseSeed)
}

func recipeSum(recipe string) string {
	sum := sha256.Sum256([]byte(recipe))
	return hex.EncodeToString(sum[:])
}

func sidecarPath(wav string) string { return wav + ".recipe" }

// ensureWAV runs synth unless wav already exists with a sidecar recording the
// current recipe hash, then records the hash.
func ensureWAV(wav, recipe string, synth func() error) error {
	want := recipeSum(recipe)
	if _, err := os.Stat(wav); err == nil {
		if got, err := os.ReadFile(sidecarPath(wav)); err == nil && strings.TrimSpace(string(got)) == want {
			return nil
		}
	}
	if err := synth(); err != nil {
		return err
	}
	return os.WriteFile(sidecarPath(wav), []byte(want+"\n"), 0o644)
}

// ensureFixtures synthesizes any stale or missing wavs via cmd/golden-tts
// (Kokoro, af_heart, 0.95 — the L4 recipe), then resamples 24 kHz -> 16 kHz and
// appends 1.5 s of trailing silence, matching the L4 fixture description.
func ensureFixtures(texts []string, dir string) ([]fixture, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	fixtures := make([]fixture, 0, len(texts))
	for i, text := range texts {
		wav := filepath.Join(dir, fmt.Sprintf("fix%02d.wav", i+1))
		raw := filepath.Join(dir, fmt.Sprintf("fix%02d.24k.wav", i+1))
		fixtures = append(fixtures, fixture{wav: wav, expected: text})
		err := ensureWAV(wav, speechRecipe(text), func() error {
			cmd := exec.Command("go", "run", "./cmd/golden-tts",
				"-text", text, "-voice", synthVoice, "-speed", synthSpeed, "-out", raw)
			if out, err := cmd.CombinedOutput(); err != nil {
				return fmt.Errorf("golden-tts %q: %w\n%s", text, err, out)
			}
			samples, sr, err := audio.ReadWAVFloat32(raw)
			if err != nil {
				return err
			}
			down := resampleLinear(samples, sr, fixtureRate)
			down = append(down, make([]float32, int(trailingSilenceS*fixtureRate))...)
			return audio.WriteWAVFloat32(wav, fixtureRate, down)
		})
		if err != nil {
			return nil, err
		}
	}
	return fixtures, nil
}

// ensureNonSpeech writes two deterministic non-speech wavs: pure silence and
// low-level noise from a fixed-seed LCG. The testdata pair spec/20 named is
// unusable for this check: barge_in.wav contains the spoken word "Okay".
func ensureNonSpeech(dir string) ([]fixture, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	n := int(trailingSilenceS * fixtureRate * 2)
	silence := filepath.Join(dir, "nonspeech-silence.wav")
	err := ensureWAV(silence, nonSpeechRecipe("silence", n), func() error {
		return audio.WriteWAVFloat32(silence, fixtureRate, make([]float32, n))
	})
	if err != nil {
		return nil, err
	}
	noise := filepath.Join(dir, "nonspeech-noise.wav")
	err = ensureWAV(noise, nonSpeechRecipe("noise", n), func() error {
		samples := make([]float32, n)
		state := uint32(noiseSeed)
		for i := range samples {
			state = state*1664525 + 1013904223
			samples[i] = (float32(state>>8)/float32(1<<24) - 0.5) * 0.1
		}
		return audio.WriteWAVFloat32(noise, fixtureRate, samples)
	})
	if err != nil {
		return nil, err
	}
	return []fixture{{wav: silence}, {wav: noise}}, nil
}

func resampleLinear(in []float32, srcRate, dstRate int) []float32 {
	if srcRate == dstRate || len(in) == 0 {
		return in
	}
	n := int(float64(len(in)) * float64(dstRate) / float64(srcRate))
	out := make([]float32, n)
	ratio := float64(srcRate) / float64(dstRate)
	for i := range out {
		pos := float64(i) * ratio
		j := int(pos)
		if j >= len(in)-1 {
			out[i] = in[len(in)-1]
			continue
		}
		frac := float32(pos - float64(j))
		out[i] = in[j]*(1-frac) + in[j+1]*frac
	}
	return out
}

type batchLine struct {
	Type     string  `json:"type"`
	LoadMS   float64 `json:"load_ms"`
	File     string  `json:"file"`
	Text     string  `json:"text"`
	MelMS    float64 `json:"mel_ms"`
	EncodeMS float64 `json:"encode_ms"`
	DecodeMS float64 `json:"decode_ms"`
}

type batchResult struct {
	fix       fixture
	text      string
	computeNS int64
}

func runBatch(o options, fixtures []fixture) ([]batchResult, error) {
	byPath := make(map[string]fixture, len(fixtures))
	var list strings.Builder
	for _, f := range fixtures {
		abs, err := filepath.Abs(f.wav)
		if err != nil {
			return nil, err
		}
		byPath[abs] = f
		list.WriteString(abs + "\n")
	}
	listPath := filepath.Join(os.TempDir(), fmt.Sprintf("stt-ab-batch-%d.list", os.Getpid()))
	if err := os.WriteFile(listPath, []byte(list.String()), 0o644); err != nil {
		return nil, err
	}
	defer os.Remove(listPath)

	cmd := exec.Command(o.cli, "-q", "-m", o.model, "--backend", o.backend, "--batch", listPath, "--batch-jsonl")
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("transcribe-cli --backend %s: %w\n%s", o.backend, err, stderr.String())
	}

	return parseBatchOutput(out, byPath, o.label, os.Stderr)
}

// parseBatchOutput turns transcribe-cli --batch-jsonl output into results. The
// header line only carries the one-off model load, which is deliberately not in
// any per-file latency. Every fixture must come back exactly once.
func parseBatchOutput(out []byte, byPath map[string]fixture, label string, logw io.Writer) ([]batchResult, error) {
	var results []batchResult
	sc := bufio.NewScanner(strings.NewReader(string(out)))
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		if strings.TrimSpace(sc.Text()) == "" {
			continue
		}
		var line batchLine
		if err := json.Unmarshal(sc.Bytes(), &line); err != nil {
			return nil, fmt.Errorf("parse batch line %q: %w", sc.Text(), err)
		}
		if line.Type == "batch_header" {
			fmt.Fprintf(logw, "%s: model load %.1f ms (excluded from per-file latency)\n", label, line.LoadMS)
			continue
		}
		f, ok := byPath[line.File]
		if !ok {
			return nil, fmt.Errorf("batch output for unknown file %s", line.File)
		}
		results = append(results, batchResult{
			fix:       f,
			text:      line.Text,
			computeNS: int64((line.MelMS + line.EncodeMS + line.DecodeMS) * 1e6),
		})
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if len(results) != len(byPath) {
		return nil, fmt.Errorf("expected %d batch results, got %d", len(byPath), len(results))
	}
	return results, nil
}

// transcriptScore mirrors cmd/obey-voice/cmd/benchmark.go transcriptScore
// exactly; candidate scores must be computed the same way as the control's or
// the accuracy gap is meaningless.
func transcriptScore(expected, actual string) float64 {
	exp := tokenizeTranscript(expected)
	act := tokenizeTranscript(actual)
	if len(exp) == 0 {
		if len(act) == 0 {
			return 1
		}
		return 0
	}
	expCounts := make(map[string]int, len(exp))
	for _, token := range exp {
		expCounts[token]++
	}
	matches := 0
	for _, token := range act {
		if expCounts[token] > 0 {
			matches++
			expCounts[token]--
		}
	}
	precision := float64(matches) / float64(max(len(act), 1))
	recall := float64(matches) / float64(len(exp))
	if precision+recall == 0 {
		return 0
	}
	return 2 * precision * recall / (precision + recall)
}

func tokenizeTranscript(text string) []string {
	text = strings.ToLower(strings.TrimSpace(text))
	if text == "" {
		return nil
	}
	clean := strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.IsSpace(r) {
			return r
		}
		return ' '
	}, text)
	return strings.Fields(clean)
}
