package pipeline

import (
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/tts"
)

type vocalClip struct {
	kind    string
	samples []float32
	rate    int
}

// vocalBank holds optional phrase rotation for engines that opt into vocal
// tags. Recorded WAVs are not mixed into TTS.
type vocalBank struct {
	mu         sync.Mutex
	clips      map[string][]vocalClip
	last       map[string]int
	lastPhrase map[string]int
	rnd        *rand.Rand
}

func newVocalBank(dir string) *vocalBank {
	b := &vocalBank{
		clips:      make(map[string][]vocalClip),
		last:       make(map[string]int),
		lastPhrase: make(map[string]int),
		rnd:        rand.New(rand.NewSource(time.Now().UnixNano())),
	}
	if dir != "" {
		b.loadDir(dir)
	}
	return b
}

func (b *vocalBank) loadDir(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	loaded := make(map[string][]vocalClip)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(strings.ToLower(name), ".wav") {
			continue
		}
		kind := vocalKindFromFilename(name)
		if kind == "" {
			continue
		}
		samples, rate, err := audio.ReadWAVFloat32(filepath.Join(dir, name))
		if err != nil || len(samples) == 0 || rate <= 0 {
			continue
		}
		if rate != vocalSampleRate {
			samples = resampleLinear(samples, rate, vocalSampleRate)
			rate = vocalSampleRate
		}
		loaded[kind] = append(loaded[kind], vocalClip{kind: kind, samples: samples, rate: rate})
	}
	for kind, clips := range loaded {
		if len(clips) > 0 {
			b.clips[kind] = clips
		}
	}
}

func vocalKindFromFilename(name string) string {
	base := strings.TrimSuffix(strings.ToLower(name), ".wav")
	base = strings.Split(base, "-")[0]
	base = strings.Split(base, "_")[0]
	return tts.CanonicalVocal(base)
}

func (b *vocalBank) nextPhrase(kind string) string {
	phrases := vocalPhrases[kind]
	if len(phrases) == 0 {
		return ""
	}
	if b == nil || len(phrases) == 1 {
		return phrases[0]
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	idx := b.rnd.Intn(len(phrases))
	if idx == b.lastPhrase[kind] {
		idx = (idx + 1) % len(phrases)
	}
	b.lastPhrase[kind] = idx
	return phrases[idx]
}

func resampleLinear(in []float32, from, to int) []float32 {
	if from == to || from <= 0 || to <= 0 || len(in) == 0 {
		return in
	}
	n := int(float64(len(in)) * float64(to) / float64(from))
	if n < 1 {
		n = 1
	}
	out := make([]float32, n)
	ratio := float64(from) / float64(to)
	last := len(in) - 1
	for i := range out {
		src := float64(i) * ratio
		j := int(src)
		if j >= last {
			out[i] = in[last]
			continue
		}
		frac := float32(src - float64(j))
		out[i] = in[j]*(1-frac) + in[j+1]*frac
	}
	return out
}
