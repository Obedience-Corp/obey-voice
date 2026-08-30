package pipeline

import (
	"strings"
	"testing"
)

// speakAll runs a whole segment list through one projection and returns the
// text each segment contributed to synthesis, dropping the silent ones.
func speakAll(p *SpeechProjection, segments ...string) []string {
	var heard []string
	for _, seg := range segments {
		if out := p.Speak(seg); out.Heard() {
			heard = append(heard, out.Speakable)
		}
	}
	if out := p.Flush(); out.Heard() {
		heard = append(heard, out.Speakable)
	}
	return heard
}

// TestSpeechProjectionGatesLeadingRegions is the coverage PR #71's review asked
// for: a reply that opens on tool output or a fenced block must not report its
// first segment as spoken. A projection that applied only the think filter and
// cleaning — the shape a benchmark reaches for — would time the tool line as
// the first word a listener hears, which is the opposite of what happens.
func TestSpeechProjectionGatesLeadingRegions(t *testing.T) {
	t.Run("leading tool region is silent until prose resumes", func(t *testing.T) {
		heard := speakAll(NewSpeechProjection(),
			"I called the run_command tool.",
			"Process exited with code 0",
			"The capital of Australia is Canberra.",
		)
		if len(heard) != 1 {
			t.Fatalf("expected one spoken segment, got %d: %q", len(heard), heard)
		}
		if !strings.Contains(heard[0], "Canberra") {
			t.Errorf("the prose after the tool region is what is spoken, got %q", heard[0])
		}
	})

	t.Run("leading fenced block is silent", func(t *testing.T) {
		heard := speakAll(NewSpeechProjection(),
			"```",
			"cgo p && ls -1 /Users/someone/projects/",
			"```",
			"That listed the projects for you.",
		)
		if len(heard) != 1 || !strings.Contains(heard[0], "listed the projects") {
			t.Fatalf("only the prose may be spoken, got %q", heard)
		}
	})

	t.Run("the gate reports what it removed", func(t *testing.T) {
		p := NewSpeechProjection()
		out := p.Speak("Running: cgo p")
		if out.Heard() {
			t.Errorf("a command line must not be spoken, got %q", out.Speakable)
		}
		if out.Suppressed == 0 {
			t.Error("a suppressed line must be counted, or a leak is invisible in the artifact")
		}
	})
}

// TestSpeechProjectionMatchesTheTurnLoopRules covers the rest of the path the
// turn loop applies between a raw segment and the synthesizer, so the harness
// timing the projection is timing the product.
func TestSpeechProjectionMatchesTheTurnLoopRules(t *testing.T) {
	t.Run("reasoning is dropped and the reply survives", func(t *testing.T) {
		p := NewSpeechProjection()
		if out := p.Speak("<think>The user asked about Australia."); out.Heard() {
			t.Errorf("reasoning must not be spoken, got %q", out.Speakable)
		}
		if out := p.Speak("Canberra is the answer.</think>"); out.Heard() {
			t.Errorf("reasoning must not be spoken, got %q", out.Speakable)
		}
		out := p.Speak("It's Canberra.")
		if !out.Heard() || !strings.Contains(out.Speakable, "Canberra") {
			t.Fatalf("the reply after the think region must be spoken, got %+v", out)
		}
		if out.Filtered == "" {
			t.Error("Filtered carries the pre-cleaning text the leak check reads")
		}
	})

	t.Run("markdown is cleaned before the text is measured", func(t *testing.T) {
		out := NewSpeechProjection().Speak("Here is **bold** text.")
		if strings.Contains(out.Speakable, "**") {
			t.Errorf("CleanForVoice runs before the text is recorded, got %q", out.Speakable)
		}
	})

	t.Run("a filler-only segment is not a spoken word", func(t *testing.T) {
		if out := NewSpeechProjection().Speak("Hmm."); out.Heard() {
			t.Errorf("a bare hesitation must not start the clock, got %q", out.Speakable)
		}
	})

	t.Run("a line repeated after a phantom tool is spoken once", func(t *testing.T) {
		heard := speakAll(NewSpeechProjection(),
			"The final is on Sunday.",
			"The final is on Sunday.",
		)
		if len(heard) != 1 {
			t.Fatalf("a restated line must not be spoken twice, got %q", heard)
		}
	})

	t.Run("state spans segments, so one projection per turn", func(t *testing.T) {
		fresh := NewSpeechProjection()
		if out := fresh.Speak("cgo p && ls -1 /Users/someone/projects/"); out.Heard() {
			t.Errorf("machine output must not be spoken even without a marker, got %q", out.Speakable)
		}
	})
}
