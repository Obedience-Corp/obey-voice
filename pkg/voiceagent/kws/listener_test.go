package kws

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
)

func TestListenerNeedsTheModel(t *testing.T) {
	if _, err := NewListener(t.TempDir(), nil); err == nil {
		t.Fatal("accepted missing model")
	}
}

// One stream, fed the way a microphone feeds it: small chunks, utterances
// separated by quiet, nothing reset between them.
func TestListenerHearsNamesInOneContinuousStream(t *testing.T) {
	dir := os.Getenv("OBEY_KWS_MODEL_DIR")
	if dir == "" {
		t.Skip("set OBEY_KWS_MODEL_DIR to the extracted keyword model")
	}
	listener, err := NewListener(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	written := time.Duration(0)
	write := func(samples []float32) []Hit {
		t.Helper()
		var hits []Hit
		for len(samples) > 0 {
			n := min(len(samples), sampleRate/50) // a 20 ms capture callback
			got, err := listener.Write(samples[:n])
			if err != nil {
				t.Fatal(err)
			}
			hits = append(hits, got...)
			samples = samples[n:]
			written += time.Duration(n) * time.Second / sampleRate
		}
		return hits
	}
	quiet := make([]float32, 2*sampleRate)
	for _, tc := range []struct {
		file string
		hits int
	}{
		{"british", 1}, {"negative", 0}, {"positive", 1}, {"other-keyword", 0}, {"kokoro", 1}, {"mention", 1},
	} {
		samples, _, err := audio.ReadWAVFloat32(filepath.Join("../../../testdata/kws", tc.file+".wav"))
		if err != nil {
			t.Fatal(err)
		}
		before := written
		hits := append(write(samples), write(quiet)...)
		t.Logf("%s: %+v (stream at %s to %s)", tc.file, hits, before, written)
		if len(hits) != tc.hits {
			t.Fatalf("%s: %d hits, want %d", tc.file, len(hits), tc.hits)
		}
		for _, hit := range hits {
			// By counts from the start of the stream, not of the utterance.
			if hit.Name != DefaultName || hit.By <= before || hit.By > written {
				t.Fatalf("%s: hit %+v outside %s..%s", tc.file, hit, before, written)
			}
		}
	}

	// Reset drops what was heard, and the clock keeps running.
	samples, _, err := audio.ReadWAVFloat32("../../../testdata/kws/british.wav")
	if err != nil {
		t.Fatal(err)
	}
	if hits := write(samples[:len(samples)/8]); len(hits) != 0 {
		t.Fatalf("heard the name in its first eighth: %+v", hits)
	}
	listener.Reset()
	before := written
	if hits := append(write(samples), write(quiet)...); len(hits) != 1 || hits[0].By <= before {
		t.Fatalf("after Reset: %+v, stream was at %s", hits, before)
	}

	listener.Close()
	listener.Close()
	if _, err := listener.Write(quiet); err == nil {
		t.Fatal("closed listener accepted audio")
	}
	listener.Reset()
}
