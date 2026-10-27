// Package kws supplies acoustic evidence for name routing, not permission to
// answer. An ordinary use of the verb "obey" is also an acoustic match.
package kws

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	sherpa "github.com/k2-fsa/sherpa-onnx-go/sherpa_onnx"
)

const sampleRate = 16000

// Obey owns model weights; every Detect call owns a fresh native stream.
type Obey struct {
	mu      sync.Mutex
	spotter *sherpa.KeywordSpotter
}

// NewObey loads the managed model directory, without downloading or capturing
// audio. Tokenization of OBEY was verified against the archive's bpe.model.
func NewObey(dir string) (*Obey, error) {
	files := config.AcousticNameAsset().CheckFiles
	for _, name := range files {
		if st, err := os.Stat(filepath.Join(dir, name)); err != nil || st.IsDir() {
			return nil, fmt.Errorf("acoustic name model missing %s; run 'obey-voice models ensure --stt'", name)
		}
	}
	keywords := "▁O B E Y @Obey\n"
	cfg := sherpa.KeywordSpotterConfig{
		FeatConfig: sherpa.FeatureConfig{SampleRate: sampleRate, FeatureDim: 80},
		ModelConfig: sherpa.OnlineModelConfig{
			Transducer: sherpa.OnlineTransducerModelConfig{
				Encoder: filepath.Join(dir, files[0]), Decoder: filepath.Join(dir, files[1]), Joiner: filepath.Join(dir, files[2]),
			},
			Tokens: filepath.Join(dir, "tokens.txt"), NumThreads: 1, Provider: "cpu", ModelType: "zipformer2",
		},
		MaxActivePaths: 4, KeywordsScore: 1, KeywordsThreshold: 0.25,
		KeywordsBuf: keywords, KeywordsBufSize: len(keywords),
	}
	spotter := sherpa.NewKeywordSpotter(&cfg)
	if spotter == nil {
		return nil, fmt.Errorf("create acoustic name detector; run 'obey-voice models ensure --stt'")
	}
	return &Obey{spotter: spotter}, nil
}

// Detect accepts normalized mono 16 kHz audio from the finalized VAD utterance.
// It never rewrites or consumes the caller's samples. The returned name is an
// observation, not confidence, onset time, or a decision that we were addressed.
func (o *Obey) Detect(ctx context.Context, samples []float32) (string, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if o.spotter == nil {
		return "", fmt.Errorf("acoustic name detector is closed")
	}
	if len(samples) == 0 {
		return "", nil
	}
	stream := sherpa.NewKeywordStream(o.spotter)
	if stream == nil {
		return "", fmt.Errorf("create acoustic name stream")
	}
	defer sherpa.DeleteOnlineStream(stream)
	// The trailing half-second flushes model look-ahead; it does not introduce
	// a wall-clock wait or get appended to the STT audio.
	for _, span := range [][]float32{samples, make([]float32, sampleRate/2)} {
		for len(span) > 0 {
			if err := ctx.Err(); err != nil {
				return "", err
			}
			n := min(len(span), 3200)
			stream.AcceptWaveform(sampleRate, span[:n])
			span = span[n:]
			for o.spotter.IsReady(stream) {
				if err := ctx.Err(); err != nil {
					return "", err
				}
				o.spotter.Decode(stream)
				if o.spotter.GetResult(stream).Keyword != "" {
					o.spotter.Reset(stream)
					return "Obey", ctx.Err()
				}
			}
		}
	}
	return "", ctx.Err()
}

func (o *Obey) Close() {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.spotter != nil {
		sherpa.DeleteKeywordSpotter(o.spotter)
		o.spotter = nil
	}
}
