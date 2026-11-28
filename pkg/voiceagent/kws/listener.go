package kws

import (
	"fmt"
	"strconv"
	"sync"
	"time"

	sherpa "github.com/k2-fsa/sherpa-onnx-go/sherpa_onnx"
)

// Listener hears names in audio as it arrives, for a wake word that reacts
// while the user is still speaking. A Spotter judges one finished utterance;
// a Listener is fed a microphone for as long as the program runs.
//
// It is usable by itself: a host with its own audio loop writes samples and
// acts on the hits. voiceagent.New runs one for Options.NameSummonLive.
//
// A hit is the same kind of evidence a Spotter gives, and weaker. On one
// continuous stream the model heard 22 of 24 clean synthesized "Juno" clips
// where a fresh stream per utterance heard all 24, and it matches a name
// inside a sentence ("tell me about Juno") and near-homophones ("Juneau") just
// as readily. Use it to chime or light up at once; let a transcript decide
// whether to answer when there is one.
//
// A Listener owns its model weights and one native stream. It is safe for
// concurrent use, though audio must of course be written in order.
type Listener struct {
	mu      sync.Mutex
	spotter *Spotter
	stream  *sherpa.OnlineStream
	fed     int64
}

// NewListener loads the keyword model in dir and listens for names, with the
// rules of New: no names means DefaultName, and a name the model cannot spell
// is ErrUnspellable.
func NewListener(dir string, names []string) (*Listener, error) {
	spotter, err := New(dir, names)
	if err != nil {
		return nil, err
	}
	stream := sherpa.NewKeywordStream(spotter.spotter)
	if stream == nil {
		spotter.Close()
		return nil, fmt.Errorf("create acoustic name stream")
	}
	return &Listener{spotter: spotter, stream: stream}, nil
}

// Names reports what the listener listens for.
func (l *Listener) Names() []string {
	return l.spotter.Names()
}

// Write takes the next normalized mono 16 kHz samples and reports the names
// completed in them, oldest first; usually none. It does not keep or change
// samples. Hit.By is the position in everything written since NewListener,
// and has the lag described on Hit.
//
// The cost is that of the model, about 2% of one desktop core for audio
// arriving in real time. Write returns once the samples are decoded, so call
// it off the audio callback.
func (l *Listener) Write(samples []float32) ([]Hit, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.stream == nil {
		return nil, fmt.Errorf("acoustic name listener is closed")
	}
	var hits []Hit
	for len(samples) > 0 {
		n := min(len(samples), feedSamples)
		l.stream.AcceptWaveform(sampleRate, samples[:n])
		samples = samples[n:]
		l.fed += int64(n)
		for l.spotter.spotter.IsReady(l.stream) {
			l.spotter.spotter.Decode(l.stream)
			tag := l.spotter.spotter.GetResult(l.stream).Keyword
			if tag == "" {
				continue
			}
			// sherpa-onnx asks for a reset right after a match, so the
			// matched audio cannot count toward the next one.
			l.spotter.spotter.Reset(l.stream)
			index, err := strconv.Atoi(tag)
			if err != nil || index < 0 || index >= len(l.spotter.names) {
				return hits, fmt.Errorf("acoustic name detector reported unknown keyword %q", tag)
			}
			hits = append(hits, Hit{Name: l.spotter.names[index], By: streamTime(l.fed)})
		}
	}
	return hits, nil
}

// streamTime is how much audio n samples are. Whole seconds are taken first:
// n times a second in nanoseconds overflows after about 160 hours of audio,
// and a listener runs for as long as the device is on.
func streamTime(n int64) time.Duration {
	return time.Duration(n/sampleRate)*time.Second + time.Duration(n%sampleRate)*time.Second/sampleRate
}

// Reset forgets the audio heard so far without moving the clock Hit.By reads.
// Call it when samples were lost or the microphone changed, so the end of one
// stretch of audio and the start of the next are not heard as one sound.
func (l *Listener) Reset() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.stream != nil {
		l.spotter.spotter.Reset(l.stream)
	}
}

// Close releases the stream and the model. A closed Listener rejects Write.
func (l *Listener) Close() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.stream != nil {
		sherpa.DeleteOnlineStream(l.stream)
		l.stream = nil
		l.spotter.Close()
	}
}
