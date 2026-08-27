package speaker

import "context"

// Diarizer is the offline meeting-review seam: full-buffer speaker
// segmentation, run in the background once capture has stopped.
//
// Engine satisfies it, so the default review path is the pyannote + TitaNet
// implementation SherpaEngine already carries and nothing has to be injected.
// speaker.meeting.diarizer selects an alternative (SortformerDiarizer), which
// replaces this one step only: live provisional labels, embeddings, enrollment
// and identity all stay on the engine.
type Diarizer interface {
	// Diarize segments samples (16 kHz mono) into labeled spans. numSpeakers is
	// a hint, not a contract — an implementation with no speaker-count input
	// declares FixedSpeakerCount rather than faking one.
	Diarize(ctx context.Context, samples []float32, numSpeakers int) (Timeline, error)
}

// FixedSpeakerCount is optionally implemented by a diarizer whose speaker count
// comes from the model rather than from clustering that numSpeakers steers.
//
// It exists so numSpeakers cannot secretly control something else. The sherpa
// engine reads the value two ways — as its cluster count, and as the auto/fixed
// switch that gates MergeSparseLabels — and a diarizer that ignores the first
// must not silently keep the second, or "expected speakers" would go on
// relabelling meetings for a model that never saw it.
type FixedSpeakerCount interface {
	// SpeakerCountIsFixed reports that numSpeakers reaches nothing.
	SpeakerCountIsFixed() bool
}

// speakerCountIsFixed reports whether numSpeakers steers d at all. Diarizers
// that do not implement the interface are steerable, which keeps the sherpa
// engine's behaviour exactly as it was.
func speakerCountIsFixed(d Diarizer) bool {
	fixed, ok := d.(FixedSpeakerCount)
	return ok && fixed.SpeakerCountIsFixed()
}
