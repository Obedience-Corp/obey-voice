package utterance

import (
	"errors"
	"strings"
	"testing"
)

func validInput() Finalized {
	return Finalized{ID: "utterance-1", CaptureEpoch: 1, Audio: &AudioRef{ID: strings.Repeat("a", 64), MIME: "audio/wav", SampleRate: 16000, Channels: 1, SampleCount: 3}, Transcript: Transcript{State: Pending}}
}
func TestAudioAloneFinalizesWithoutFabricatedTranscript(t *testing.T) {
	f := validInput()
	if e := f.Validate(); e != nil {
		t.Fatal(e)
	}
	for _, state := range []State{Pending, Unavailable, Disabled} {
		f.Transcript = Transcript{State: state, Text: "invented"}
		if f.Validate() == nil {
			t.Fatalf("non-ready %s accepted text", state)
		}
	}
	f.Transcript = Transcript{State: Ready}
	if f.Validate() == nil {
		t.Fatal("empty ready transcript accepted")
	}
	f.Transcript = Transcript{State: Ready, Text: "Actual user transcript", Provider: "ollama", Model: "lfm3-audio:2b"}
	if e := f.Validate(); e != nil {
		t.Fatal(e)
	}
}
func TestMalformedIncompleteAndOversizedAudioAreRejected(t *testing.T) {
	for _, name := range []string{"missing", "hash", "empty", "rate", "channels", "too_long", "gap", "overflow", "span_count", "flag"} {
		t.Run(name, func(t *testing.T) {
			f := validInput()
			switch name {
			case "missing":
				f.Audio = nil
			case "hash":
				f.Audio.ID = "../bad"
			case "empty":
				f.Audio.SampleCount = 0
			case "rate":
				f.Audio.SampleRate = 24000
			case "channels":
				f.Audio.Channels = 2
			case "too_long":
				f.Audio.SampleCount = MaxSamples + 1
			case "gap":
				f.Spans = []Span{{SampleOffset: 0, SampleCount: 1}, {SampleOffset: 2, SampleCount: 2}}
			case "overflow":
				f.Spans = []Span{{SampleOffset: ^uint64(0), SampleCount: 3}}
			case "span_count":
				f.Spans = []Span{{SampleOffset: 0, SampleCount: 2}}
			case "flag":
				f.Incomplete = true
			}
			if e := f.Validate(); e == nil {
				t.Fatal("incomplete audio accepted")
			}
		})
	}
}
func TestUnknownGenerationDoesNotHideKnownDeviceTransition(t *testing.T) {
	for _, last := range []uint64{1, 2} {
		f := validInput()
		f.Spans = []Span{{SampleOffset: 10, SampleCount: 1, DeviceGeneration: 1}, {SampleOffset: 11, SampleCount: 1}, {SampleOffset: 12, SampleCount: 1, DeviceGeneration: last}}
		e := f.Validate()
		if last == 1 && e != nil {
			t.Fatal(e)
		}
		if last == 2 && !errors.Is(e, ErrIncomplete) {
			t.Fatalf("known transition hidden by unknown span: %v", e)
		}
	}
}
