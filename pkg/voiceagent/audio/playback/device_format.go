package playback

import (
	"fmt"

	"github.com/gen2brain/malgo"
)

// playbackDeviceDetails returns the selected device's display name, a preferred
// native sample rate, and the channel count advertised by its shared-mode
// formats. sourceRate is the TTS stream rate (Kokoro is 24 kHz): when the
// device offers an integer multiple of that rate (e.g. 48 kHz), we prefer it
// over 44.1 kHz so Obey Voice can upsample with an exact ratio instead of an
// irrational 24→44.1 conversion that linear/cubic interpolation renders as
// harsh/crackly HF on multi-channel displays.
//
// Opening CoreAudio at Kokoro's 24 kHz mono rate delegates rate and layout
// conversion to the backend; on Studio Display Speakers (8 ch @ 44.1/48 kHz)
// that path produced audible crackle. Obey Voice opens at a preferred device
// rate with a stereo client layout and expands mono TTS before the callback.
func playbackDeviceDetails(ctx malgo.Context, name string, sourceRate int) (string, uint32, uint32, error) {
	infos, err := ctx.Devices(malgo.Playback)
	if err != nil {
		return "", 0, 0, fmt.Errorf("enumerate playback devices: %w", err)
	}

	var selected *malgo.DeviceInfo
	for i := range infos {
		if (name != "" && infos[i].Name() == name) || (name == "" && infos[i].IsDefault != 0) {
			selected = &infos[i]
			break
		}
	}
	if selected == nil && name == "" && len(infos) > 0 {
		selected = &infos[0]
	}
	if selected == nil {
		if name != "" {
			return "", 0, 0, fmt.Errorf("audio device %q is not available", name)
		}
		return "", 0, 0, nil
	}

	full, err := ctx.DeviceInfo(malgo.Playback, selected.ID, malgo.Shared)
	if err != nil {
		return selected.Name(), 0, 0, fmt.Errorf("query playback device %q: %w", selected.Name(), err)
	}
	rate, channels := pickPlaybackFormat(full.Formats, sourceRate)
	return selected.Name(), rate, channels, nil
}

// pickPlaybackFormat chooses a sample rate and channel count from the device's
// shared-mode formats. sourceRate is the TTS PCM rate (0 if unknown).
//
// Used as a *hint* for openPlaybackDevice's fallback order. Actual open order
// prefers the TTS rate first (see playbackRateCandidates). Among advertised
// formats, prefer 44.1 kHz over 48 kHz: the 24→48 cubic path was worse on the
// affected Studio Display, and 44.1 is the first clock Apple lists for that
// device.
func pickPlaybackFormat(formats []malgo.DataFormat, sourceRate int) (rate uint32, channels uint32) {
	bestScore := -1
	for _, format := range formats {
		if format.SampleRate == 0 || format.Channels == 0 {
			continue
		}
		score := 0
		fr := int(format.SampleRate)
		switch {
		case sourceRate > 0 && fr == sourceRate:
			score += 300
		case fr == 44100:
			score += 120
		case fr == 48000:
			score += 90
		case sourceRate > 0 && fr%sourceRate == 0:
			score += 70
		case fr == 96000 || fr == 88200:
			score += 40
		default:
			score += 10
		}
		switch format.Channels {
		case 2:
			score += 50
		case 1:
			score += 30
		default:
			score += 10
		}
		score -= int(format.Channels)
		if score > bestScore {
			bestScore = score
			rate = format.SampleRate
			channels = format.Channels
		}
	}
	return rate, channels
}

// choosePlaybackChannels picks the client layout Obey Voice opens on the device.
// Voice is mono; we open stereo when the device allows a 2ch client format so
// L/R carry the same signal. When the device only advertises multi-channel
// layouts (Studio Display Speakers lists 8ch), open at that count and map mono
// onto the front L/R pair with silence on the remaining channels — leaving
// CoreAudio to invent a mono→8ch conversion has been a crackle source.
func choosePlaybackChannels(nativeChannels uint32) uint32 {
	switch {
	case nativeChannels == 0:
		return 2
	case nativeChannels == 1:
		return 1
	case nativeChannels == 2:
		return 2
	default:
		// Device is multi-channel. Prefer a stereo client format: CoreAudio
		// accepted Channels=2 on Studio Display Speakers in hardware probes
		// even though shared formats listed only 8ch.
		return 2
	}
}
