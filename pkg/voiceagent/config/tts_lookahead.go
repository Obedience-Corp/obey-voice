package config

// How many admitted-but-unplayed speech segments the turn may hold
// (tts_lookahead). Depth 2 is one segment playing and one synthesizing, which
// is what the pipeline has always done; it is a bound on lookahead, not a
// target, and the speech engine's own concurrency raises it when it is higher.
const (
	DefaultTTSLookahead = 2
	MinTTSLookahead     = 1
	MaxTTSLookahead     = 8
)

// TTSLookaheadOf clamps cfg's configured lookahead depth to the supported
// range, falling back to the default for an unset or out-of-range value —
// the same posture as the barge-in windows.
func TTSLookaheadOf(cfg *Config) int {
	if cfg == nil {
		return DefaultTTSLookahead
	}
	if cfg.TTSLookahead < MinTTSLookahead || cfg.TTSLookahead > MaxTTSLookahead {
		return DefaultTTSLookahead
	}
	return cfg.TTSLookahead
}
