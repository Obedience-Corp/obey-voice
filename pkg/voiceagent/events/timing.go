package events

// TurnTiming retains measured offsets from turn start and a projection using
// one declared origin. Values are milliseconds, not stage durations. Nil means
// the boundary was not observed; a measured zero remains distinguishable.
// StreamDrainMS is host transport completion, never remote playback evidence.
type TurnTiming struct {
	Origin         string           `json:"origin"`
	OriginOffsetMS float64          `json:"origin_offset_ms"`
	Raw            TimingMilestones `json:"raw_turn_start"`
	Normalized     TimingMilestones `json:"normalized"`
}

type TimingMilestones struct {
	FinalizedInputMS   *float64 `json:"finalized_input_ms"`
	FirstModelChunkMS  *float64 `json:"first_model_chunk_ms"`
	ModelCompleteMS    *float64 `json:"model_complete_ms"`
	FirstAudioReadyMS  *float64 `json:"first_audio_ready_ms"`
	PlaybackStartMS    *float64 `json:"playback_start_ms"`
	PlaybackCompleteMS *float64 `json:"playback_complete_ms"`
	StreamDrainMS      *float64 `json:"stream_drain_ms"`
	BargeInMS          *float64 `json:"barge_in_ms"`
}
