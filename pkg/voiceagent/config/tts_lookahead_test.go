package config

import "testing"

func TestTTSLookaheadOfClampsOutOfRange(t *testing.T) {
	for _, tt := range []struct {
		name  string
		value int
		want  int
	}{
		{"unset (zero value)", 0, DefaultTTSLookahead},
		{"negative", -1, DefaultTTSLookahead},
		{"above max", 64, DefaultTTSLookahead},
		{"at min", MinTTSLookahead, MinTTSLookahead},
		{"at max", MaxTTSLookahead, MaxTTSLookahead},
		{"in range", 3, 3},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := TTSLookaheadOf(&Config{TTSLookahead: tt.value}); got != tt.want {
				t.Errorf("TTSLookaheadOf(%d) = %d, want %d", tt.value, got, tt.want)
			}
		})
	}
	if got := TTSLookaheadOf(nil); got != DefaultTTSLookahead {
		t.Errorf("TTSLookaheadOf(nil) = %d, want %d", got, DefaultTTSLookahead)
	}
}
