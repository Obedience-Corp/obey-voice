package pipeline

import (
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/work"
)

// completionPlayback is owned by the serialized response loop. It records
// observed segment receipts, not synthesis or network enqueue success.
type completionPlayback struct {
	lease            work.CompletionLease
	queued, finished int
	skipped, failed  bool
	seen             map[int]bool
}

func (d *completionPlayback) observe(e playbackEvent, sink string) {
	if e.kind == playbackNotEnqueued {
		d.failed = true
		return
	}
	if e.kind != playbackFinished {
		return
	}
	if d.seen == nil {
		d.seen = make(map[int]bool)
	}
	if d.seen[e.index] {
		d.failed = true
		return
	}
	d.seen[e.index] = true
	d.finished++
	r := e.result
	if sink != audio.SinkLocalSpeaker || r.AttemptID != d.lease.AttemptID || r.CaptureEpoch != d.lease.CaptureEpoch || r.Err != nil || r.Interrupted || r.AudioSeconds <= 0 || e.error != "" {
		d.failed = true
	}
}

func (d *completionPlayback) complete() bool {
	return d != nil && d.queued > 0 && d.finished == d.queued && !d.failed && !d.skipped
}
