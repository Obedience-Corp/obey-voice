package config

// How many Qwen engine instances synthesize at once (qwen_tts_workers). One
// bin/qwen3-tts-worker subprocess holds its session for a whole request, so a
// second concurrent segment waits on it; a second subprocess is the only way
// to raise throughput without changing the engine.
//
// The count is clamped to 1..4 because every worker loads its own copy of the
// model onto the same GPU.
const (
	DefaultQwenTTSWorkers = 1
	MinQwenTTSWorkers     = 1
	MaxQwenTTSWorkers     = 4
)

// QwenTTSWorkersOf clamps cfg's configured worker count to the supported
// range, falling back to the default for an unset or out-of-range value — the
// same posture as TTSLookaheadOf.
//
// It answers for the key alone. Whether the count is honoured is the
// provider's decision: only a local native pool runs more than one engine
// (managed Python is a single legacy session, and a LAN worker's parallelism
// belongs to the speech host).
func QwenTTSWorkersOf(cfg *Config) int {
	if cfg == nil {
		return DefaultQwenTTSWorkers
	}
	if cfg.QwenTTSWorkers < MinQwenTTSWorkers || cfg.QwenTTSWorkers > MaxQwenTTSWorkers {
		return DefaultQwenTTSWorkers
	}
	return cfg.QwenTTSWorkers
}
