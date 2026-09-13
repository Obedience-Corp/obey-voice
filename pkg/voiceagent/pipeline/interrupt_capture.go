package pipeline

import (
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
)

// trimInterruptedCapture removes older response audio before the next ordinary
// listen inherits the capture buffer. A yielded STT session owns its own input
// and must never have that input trimmed by the response loop.
func (p *Pipeline) trimInterruptedCapture(request interruptRequest) {
	if !request.OnsetPositionKnown {
		return
	}
	retained, ok := p.Capture.(interface{ KeepFrom(uint64) })
	if !ok {
		return
	}
	p.captureMu.Lock()
	defer p.captureMu.Unlock()
	if p.captureSession != nil {
		return
	}
	preRoll := uint64(p.capturePreRoll() * time.Duration(audio.SampleRate) / time.Second)
	start := uint64(0)
	if request.OnsetSampleOffset > preRoll {
		start = request.OnsetSampleOffset - preRoll
	}
	retained.KeepFrom(start)
}
