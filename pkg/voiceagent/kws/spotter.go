// Package kws supplies acoustic evidence that a name was spoken, not
// permission to answer. Saying a name is not addressing its owner: an ordinary
// use of the verb "obey", or "tell me about Juno", is also an acoustic match.
package kws

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	sherpa "github.com/k2-fsa/sherpa-onnx-go/sherpa_onnx"
)

const (
	sampleRate = 16000
	// vocabFile is the sentencepiece model shipped in the keyword archive. It
	// is what turns a name into the tokens the spotter listens for.
	vocabFile = "bpe.model"
	// feedSamples is how much audio goes in between checks for a hit, which
	// bounds how late Hit.By can be on top of the model's own look-ahead.
	feedSamples = sampleRate / 10
)

// DefaultName is what a spotter listens for when it is given no names, and
// defaultLine is how the model's vocabulary writes it.
const (
	DefaultName = "Obey"
	defaultLine = "▁O B E Y"
)

// Hit is what the spotter heard in one utterance. The zero Hit is "nothing".
type Hit struct {
	// Name is the matched name as it was given to New, never the transcript's
	// spelling of it.
	Name string
	// By is how much of the utterance had been fed in when the name matched.
	// The name was complete no later than this, and usually some tenths of a
	// second earlier: the model looks ahead and decodes in steps. It is an
	// upper bound for telling a name that opens an utterance from one inside
	// it, not an onset time.
	By time.Duration
}

// Spotter owns model weights; every call owns a fresh native stream.
type Spotter struct {
	mu      sync.Mutex
	spotter *sherpa.KeywordSpotter
	names   []string
}

// Obey is the former name of Spotter, from when it could hear one word.
type Obey = Spotter

// NewObey loads a spotter for DefaultName alone.
func NewObey(dir string) (*Spotter, error) {
	return New(dir, nil)
}

// New loads the managed model directory and listens for names, or for
// DefaultName when names is empty. It does not download or capture audio.
//
// Every name must pass CheckName. Names that differ only in case or spacing
// are one name. The model is English: it hears a name as an English speaker
// would spell it aloud.
func New(dir string, names []string) (*Spotter, error) {
	files := config.AcousticNameAsset().CheckFiles
	for _, name := range files {
		if st, err := os.Stat(filepath.Join(dir, name)); err != nil || st.IsDir() {
			return nil, fmt.Errorf("acoustic name model missing %s; run 'obey-voice models ensure --stt'", name)
		}
	}
	// The default needs nothing but its one line, verified against the
	// archive's vocabulary, so an install that listened for Obey before still
	// does whatever else is in its model directory.
	spell := func(string) (string, error) { return defaultLine, nil }
	if len(names) == 0 {
		names = []string{DefaultName}
	} else {
		speller, err := loadSpeller(filepath.Join(dir, vocabFile))
		if err != nil {
			return nil, fmt.Errorf("acoustic name model: %w; it is needed to listen for names other than %s. Run 'obey-voice models ensure --stt', or remove the model directory first if it is incomplete", err, DefaultName)
		}
		spell = speller.spell
	}
	var kept []string
	var keywords strings.Builder
	seen := map[string]bool{}
	for _, name := range names {
		line, err := spell(name)
		if err != nil {
			return nil, err
		}
		if seen[line] {
			continue
		}
		seen[line] = true
		// The tag after @ is what a match reports. An index keeps names with
		// spaces or apostrophes out of the native keyword parser.
		keywords.WriteString(line + " @" + strconv.Itoa(len(kept)) + "\n")
		kept = append(kept, strings.Join(strings.Fields(name), " "))
	}
	buf := keywords.String()
	cfg := sherpa.KeywordSpotterConfig{
		FeatConfig: sherpa.FeatureConfig{SampleRate: sampleRate, FeatureDim: 80},
		ModelConfig: sherpa.OnlineModelConfig{
			Transducer: sherpa.OnlineTransducerModelConfig{
				Encoder: filepath.Join(dir, files[0]), Decoder: filepath.Join(dir, files[1]), Joiner: filepath.Join(dir, files[2]),
			},
			Tokens: filepath.Join(dir, "tokens.txt"), NumThreads: 1, Provider: "cpu", ModelType: "zipformer2",
		},
		MaxActivePaths: 4, KeywordsScore: 1, KeywordsThreshold: 0.25,
		KeywordsBuf: buf, KeywordsBufSize: len(buf),
	}
	spotter := sherpa.NewKeywordSpotter(&cfg)
	if spotter == nil {
		return nil, fmt.Errorf("create acoustic name detector; run 'obey-voice models ensure --stt'")
	}
	return &Spotter{spotter: spotter, names: kept}, nil
}

// Names reports what the spotter listens for.
func (o *Spotter) Names() []string {
	return append([]string(nil), o.names...)
}

// Detect reports only the name Observe heard.
func (o *Spotter) Detect(ctx context.Context, samples []float32) (string, error) {
	hit, err := o.Observe(ctx, samples)
	return hit.Name, err
}

// Observe accepts normalized mono 16 kHz audio from the finalized VAD
// utterance and reports the first name heard in it. It never rewrites or
// consumes the caller's samples. A hit is an observation, not confidence or a
// decision that we were addressed.
func (o *Spotter) Observe(ctx context.Context, samples []float32) (Hit, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return Hit{}, err
	}
	if o.spotter == nil {
		return Hit{}, fmt.Errorf("acoustic name detector is closed")
	}
	if len(samples) == 0 {
		return Hit{}, nil
	}
	stream := sherpa.NewKeywordStream(o.spotter)
	if stream == nil {
		return Hit{}, fmt.Errorf("create acoustic name stream")
	}
	defer sherpa.DeleteOnlineStream(stream)
	fed := 0
	// The trailing half-second flushes model look-ahead; it does not introduce
	// a wall-clock wait or get appended to the STT audio.
	for _, span := range [][]float32{samples, make([]float32, sampleRate/2)} {
		for len(span) > 0 {
			if err := ctx.Err(); err != nil {
				return Hit{}, err
			}
			n := min(len(span), feedSamples)
			stream.AcceptWaveform(sampleRate, span[:n])
			span = span[n:]
			fed += n
			for o.spotter.IsReady(stream) {
				if err := ctx.Err(); err != nil {
					return Hit{}, err
				}
				o.spotter.Decode(stream)
				tag := o.spotter.GetResult(stream).Keyword
				if tag == "" {
					continue
				}
				o.spotter.Reset(stream)
				index, err := strconv.Atoi(tag)
				if err != nil || index < 0 || index >= len(o.names) {
					return Hit{}, fmt.Errorf("acoustic name detector reported unknown keyword %q", tag)
				}
				by := time.Duration(fed) * time.Second / sampleRate
				return Hit{Name: o.names[index], By: by}, ctx.Err()
			}
		}
	}
	return Hit{}, ctx.Err()
}

func (o *Spotter) Close() {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.spotter != nil {
		sherpa.DeleteKeywordSpotter(o.spotter)
		o.spotter = nil
	}
}
