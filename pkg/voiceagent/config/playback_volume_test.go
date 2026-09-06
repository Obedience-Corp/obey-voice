package config

import "testing"

func TestPlaybackVolumeOf(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		cfg  *Config
		want float64
	}{
		{name: "nil is unity", want: DefaultPlaybackVolume},
		{name: "zero is mute", cfg: &Config{PlaybackVolume: 0}, want: 0},
		{name: "unity", cfg: &Config{PlaybackVolume: 1}, want: 1},
		{name: "boost", cfg: &Config{PlaybackVolume: 2.5}, want: 2.5},
		{name: "max", cfg: &Config{PlaybackVolume: MaxPlaybackVolume}, want: MaxPlaybackVolume},
		{name: "below min falls back", cfg: &Config{PlaybackVolume: -0.1}, want: DefaultPlaybackVolume},
		{name: "above max falls back", cfg: &Config{PlaybackVolume: 4.1}, want: DefaultPlaybackVolume},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := PlaybackVolumeOf(tt.cfg); got != tt.want {
				t.Fatalf("PlaybackVolumeOf() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestPlaybackVolumeSchema(t *testing.T) {
	spec, ok := SpecFor("playback_volume")
	if !ok {
		t.Fatal("playback_volume missing from schema")
	}
	if spec.Type != TypeFloat {
		t.Errorf("type = %s, want float", spec.Type)
	}
	if spec.Default != DefaultPlaybackVolume {
		t.Errorf("default = %v, want %v", spec.Default, DefaultPlaybackVolume)
	}
	if spec.Min == nil || *spec.Min != MinPlaybackVolume {
		t.Errorf("min = %v, want %v", spec.Min, MinPlaybackVolume)
	}
	if spec.Max == nil || *spec.Max != MaxPlaybackVolume {
		t.Errorf("max = %v, want %v", spec.Max, MaxPlaybackVolume)
	}
	if RestartRequired("playback_volume") {
		t.Error("playback_volume should apply without restart")
	}
	if !RestartVerified("playback_volume") {
		t.Error("playback_volume claims no restart without verification")
	}
}
