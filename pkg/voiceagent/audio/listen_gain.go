package audio

import "sync"

const (
	// listenGainTargetRMS is the Ask-preview quiet-mic recipe (~0.04). Meeting
	// capture uses this AGC so far/quiet talkers reach VAD and STT. It is not
	// the conversation front-end: there is no noise suppressor, which over-gates
	// room speech.
	listenGainTargetRMS = 0.04
	listenGainMin       = 0.5
	listenGainMax       = 8.0
)

// ListenGain is AGC-only capture processing for meeting notes and other
// listen-only paths. Boosts quiet USB/room talkers up to 8× and allows a modest
// cut so an OS input-volume hike does not peg the energy gate.
type ListenGain struct {
	mu  sync.Mutex
	agc automaticGainControl
}

// NewListenGain returns the meeting-capture front-end.
func NewListenGain() *ListenGain {
	return &ListenGain{
		agc: newAutomaticGainControl(listenGainTargetRMS, listenGainMin, listenGainMax),
	}
}

// ProcessCapture applies automatic gain and returns a copy. Digital silence
// stays zero: AGC on zeros is still zeros.
func (g *ListenGain) ProcessCapture(samples []float32) []float32 {
	if len(samples) == 0 {
		return samples
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	out := append([]float32(nil), samples...)
	g.agc.Process(out)
	return out
}

// PushPlaybackReference is unused: meetings have no playback to cancel.
func (g *ListenGain) PushPlaybackReference([]float32) {}

// Close implements Frontend.
func (g *ListenGain) Close() error { return nil }
