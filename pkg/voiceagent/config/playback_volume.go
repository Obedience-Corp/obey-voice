package config

// Playback volume is a linear gain applied to speaker output. 1 is unity
// (unchanged PCM), 0 is silent, and values above 1 boost a quiet device.
const (
	DefaultPlaybackVolume = 1.0
	MinPlaybackVolume     = 0.0
	MaxPlaybackVolume     = 4.0
)

// PlaybackVolumeOf clamps cfg's playback gain to 0–4. Nil or out-of-range
// values fall back to unity rather than failing the player. 0 is mute.
func PlaybackVolumeOf(cfg *Config) float64 {
	if cfg == nil {
		return DefaultPlaybackVolume
	}
	v := cfg.PlaybackVolume
	if v < MinPlaybackVolume || v > MaxPlaybackVolume {
		return DefaultPlaybackVolume
	}
	return v
}
