package playback

import (
	"encoding/binary"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"math"
)

const (
	minVolumeGain           = 0
	maxVolumeGain           = 4
	defaultVolumeHundredths = 100
	volumeUnityHundredths   = 100
)

// SetVolume sets linear playback gain. 1 is unity, 0 is silent, values above
// 1 boost. Clamped to 0–4. Safe to call from any goroutine, including while
// a sentence is playing: the next device callback hears the new gain.
func (p *Player) SetVolume(gain float64) {
	if p == nil {
		return
	}
	p.volumeHundredths.Store(volumeHundredths(gain))
}

// Volume is the gain last passed to SetVolume (unity when unset).
func (p *Player) Volume() float64 {
	if p == nil {
		return float64(defaultVolumeHundredths) / 100
	}
	return float64(p.volumeHundredths.Load()) / 100
}

// ApplyEngineVolume sets gain on engines that support it. Unknown engines
// are ignored so fakes and remote fanouts without a local speaker stay quiet
// about a TUI slider they cannot honor.
func ApplyEngineVolume(eng audio.Engine, gain float64) {
	if vc, ok := eng.(interface{ SetVolume(float64) }); ok {
		vc.SetVolume(gain)
	}
}

func volumeHundredths(gain float64) int32 {
	if math.IsNaN(gain) || gain < minVolumeGain {
		gain = minVolumeGain
	}
	if gain > maxVolumeGain {
		gain = maxVolumeGain
	}
	return int32(math.Round(gain * 100))
}

// applyVolumeS16 scales mono S16LE in place by hundredths of unity. 100 is a
// no-op so the default path does not touch samples. The device callback must
// stay allocation-free; this walks the period buffer only.
func applyVolumeS16(buf []byte, frames int, hundredths int32) {
	if frames <= 0 || hundredths == volumeUnityHundredths {
		return
	}
	n := frames * 2
	if n > len(buf) {
		n = len(buf)
		frames = n / 2
	}
	if hundredths <= 0 {
		clearBytes(buf[:frames*2])
		return
	}
	for i := 0; i < frames; i++ {
		off := i * 2
		s := int32(int16(binary.LittleEndian.Uint16(buf[off:])))
		s = s * hundredths / 100
		if s > math.MaxInt16 {
			s = math.MaxInt16
		} else if s < math.MinInt16 {
			s = math.MinInt16
		}
		binary.LittleEndian.PutUint16(buf[off:], uint16(int16(s)))
	}
}
