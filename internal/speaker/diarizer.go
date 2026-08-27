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
	// documents that it ignores the value rather than faking it.
	Diarize(ctx context.Context, samples []float32, numSpeakers int) (Timeline, error)
}
