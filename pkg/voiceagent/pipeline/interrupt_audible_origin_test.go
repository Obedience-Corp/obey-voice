package pipeline

import "testing"

func TestInterruptQualificationStartsAtFirstAudibility(t *testing.T) {
	for _, callback := range []struct {
		name    string
		samples int
	}{
		{"10ms", 160},
		{"60ms", 960},
	} {
		t.Run(callback.name, func(t *testing.T) {
			// At gain6 these represent .0005 quiet residual and .01 audible
			// speech. A quiet neural prefix longer than the vote threshold must
			// not pre-fill qualification for the later audible callback.
			const quiet, audible = float32(.003), float32(.06)
			prefix := func(h *audibilityHarness) {
				for range bargeInMinSpeechChunks + 4 {
					h.feedCallback(quiet, true, false, callback.samples)
				}
			}
			t.Run("quiet_prefix_then_one_audible_callback_does_not_trip", func(t *testing.T) {
				h := newAudibilityHarness(t, true)
				prefix(h)
				h.feedCallback(audible, true, false, callback.samples)
				h.finishWithoutTrip()
			})
			t.Run("five_softer_continuations_complete_qualification", func(t *testing.T) {
				h := newAudibilityHarness(t, true)
				prefix(h)
				h.feedCallback(audible, true, false, callback.samples)
				// Each next callback's consumption proves the previous one did
				// not stop the watcher early. The existing audibility window
				// retains this onset while neural continuation becomes quieter.
				for range bargeInMinSpeechChunks - 1 {
					h.feedCallback(quiet, true, false, callback.samples)
				}
				h.expectTrip()
			})
			t.Run("neural_silence_requires_a_fresh_count", func(t *testing.T) {
				h := newAudibilityHarness(t, true)
				prefix(h)
				h.feedCallback(audible, true, false, callback.samples)
				for range 2 {
					h.feedCallback(quiet, true, false, callback.samples)
				}
				h.feedCallback(0, false, false, callback.samples)
				prefix(h)
				h.feedCallback(audible, true, false, callback.samples)
				for range bargeInMinSpeechChunks - 2 {
					h.feedCallback(quiet, true, false, callback.samples)
				}
				h.finishWithoutTrip()
			})
			t.Run("completed_short_segment_keeps_existing_shortcut", func(t *testing.T) {
				h := newAudibilityHarness(t, true)
				prefix(h)
				h.feedCallback(audible, true, false, callback.samples)
				h.feedCallback(0, false, true, callback.samples)
				h.expectTrip()
			})
		})
	}
}
