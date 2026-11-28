package voiceagent

import (
	"errors"
	"path/filepath"
	"sync"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/kws"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/pipeline"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/summon"
)

// nameListener is the part of kws.Listener the live wake word uses.
type nameListener interface {
	Write(samples []float32) ([]kws.Hit, error)
	Reset()
	Close()
}

// openNameListener loads the real keyword model for the gate's names.
func openNameListener(cfg *config.Config, names []string) (nameListener, error) {
	return kws.NewListener(filepath.Join(config.ModelsDirFrom(cfg), config.AcousticNameModelDir), names)
}

const (
	// liveWakeBuffer is how many capture callbacks may queue while the
	// listener is busy: several seconds at the usual 10 to 20 ms each. The
	// listener decodes about fifty times faster than real time, so it only
	// fills when the machine is starved, and then audio is dropped rather
	// than delaying the microphone.
	liveWakeBuffer = 512
	// playbackEchoTail is how long after the agent's own audio stops a hit is
	// still treated as the agent's voice: the listener reports a name some
	// tenths of a second after it was spoken, and a room rings a little.
	playbackEchoTail = time.Second
)

// frameCapture is what audio.Capture adds to pipeline.CaptureMonitor: capture
// callbacks that say when samples were lost.
type frameCapture interface {
	SubscribeFrames(buffer int) (int, <-chan audio.Frame)
	Unsubscribe(id int)
}

// liveWake feeds the microphone to a keyword listener for as long as the
// agent lives and reports each name it hears.
type liveWake struct {
	listener nameListener
	playing  func() bool
	heard    func(kws.Hit, time.Time)
	logf     func(string, ...any)
	now      func() time.Time

	lastPlayback time.Time
	failed       bool
}

// startLiveWake subscribes to capture and returns the function that ends the
// listener and releases its model.
func startLiveWake(capture pipeline.CaptureMonitor, w *liveWake) (stop func()) {
	if w.now == nil {
		w.now = time.Now
	}
	done := make(chan struct{})
	var once sync.Once
	var unsubscribe func()
	if frames, ok := capture.(frameCapture); ok {
		id, ch := frames.SubscribeFrames(liveWakeBuffer)
		unsubscribe = func() { frames.Unsubscribe(id) }
		go func() {
			defer close(done)
			for frame := range ch {
				if lostAudio(frame) {
					// The audio on either side of a hole is not one sound.
					w.listener.Reset()
				}
				w.write(frame.Samples)
			}
		}()
	} else {
		id, ch := capture.Subscribe(liveWakeBuffer)
		unsubscribe = func() { capture.Unsubscribe(id) }
		go func() {
			defer close(done)
			for samples := range ch {
				w.write(samples)
			}
		}()
	}
	return func() {
		once.Do(func() {
			unsubscribe()
			<-done
			w.listener.Close()
		})
	}
}

// lostAudio reports whether frame does not follow on from the one before:
// callbacks this subscriber was too slow to take, or a microphone that was
// reopened or changed. audio.Capture marks the first callback of a new device
// on the frame's span, and a transition lost with a dropped callback on the
// gap.
func lostAudio(frame audio.Frame) bool {
	gap := frame.CaptureGap
	if gap.SubscriptionDroppedSamples > 0 || gap.SubscriptionDeviceDiscontinuity || gap.DeviceDiscontinuity {
		return true
	}
	for _, span := range frame.CaptureSpans {
		if span.DeviceDiscontinuity {
			return true
		}
	}
	return false
}

func (w *liveWake) write(samples []float32) {
	now := w.now()
	if w.playing != nil && w.playing() {
		w.lastPlayback = now
	}
	hits, err := w.listener.Write(samples)
	if err != nil {
		if !w.failed {
			w.failed = true
			w.logf("live name listener stopped hearing: %v", err)
		}
		return
	}
	for _, hit := range hits {
		// The agent saying its own name must not wake it. Echo cancellation
		// removes most of its voice from the microphone, not all, and an
		// injected capture promises nothing. The cost: the name spoken over a
		// reply gives no cue. It is still heard by the recogniser, so barge-in
		// by name keeps working.
		if !w.lastPlayback.IsZero() && now.Sub(w.lastPlayback) < playbackEchoTail {
			continue
		}
		w.heard(hit, now)
	}
}

// installLiveWake starts the live listener for Options.NameSummonLive: each
// name heard becomes an events.NameHeard, and opens the gate's wake window
// when one was asked for.
func installLiveWake(p *pipeline.Pipeline, bus *events.Bus, gate *summon.Gate, listener nameListener, logf func(string, ...any)) (stop func(), err error) {
	if p.Capture == nil {
		listener.Close()
		return nil, errors.New("the agent has no microphone to listen on")
	}
	w := &liveWake{listener: listener, logf: logf}
	if p.Player != nil {
		w.playing = p.Player.IsPlaying
	}
	w.heard = func(hit kws.Hit, at time.Time) {
		gate.Woke()
		bus.Emit(events.NameHeard{Name: hit.Name, At: at})
	}
	return startLiveWake(p.Capture, w), nil
}
