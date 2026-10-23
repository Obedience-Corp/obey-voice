package pipeline

import (
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

func TestTurnTraceReachesExistingLogWithoutConversationContent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "native-diagnostics.log")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	previous := log.Writer()
	log.SetOutput(f)
	t.Cleanup(func() { log.SetOutput(previous); _ = f.Close() })
	p := &Pipeline{} // No event bus or experimental work coordinator required.
	id := &events.ResponseEnvelope{SessionID: "session-fixture", ResponseID: "response-fixture"}
	for _, e := range []events.Event{
		events.UserInput{Text: "private utterance"},
		events.ThinkingComplete{Envelope: id, Response: "private reply", FullResponse: "private reasoning"},
		events.SpeakingStarted{Envelope: id, Text: "private spoken words", AudibleSink: "local_speaker"},
		events.SpeakingInterrupted{Envelope: id, Reason: "barge_in", SincePlaybackStart: time.Second},
		events.TurnMetrics{Envelope: id, Outcome: "interrupted", Interrupted: true},
		events.Error{Stage: "tts", Message: "private credential in provider error"},
		events.TranscriptPartial{Text: "private partial"},
		events.AudioLevel{Source: "input", Level: .9},
	} {
		p.emit(e)
	}
	if err := f.Sync(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, want := range []string{`"event":"user_input"`, `"response_id":"response-fixture"`, `"reason":"barge_in"`, `"outcome":"interrupted"`, `"stage":"tts"`} {
		if !strings.Contains(text, want) {
			t.Errorf("diagnostic log missing %s: %s", want, text)
		}
	}
	for _, forbidden := range []string{"private", "transcript_partial", "audio_level"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("trace included content or high-rate event: %s", text)
		}
	}
}

func TestBargeInTraceCapturesDecisionAtQualification(t *testing.T) {
	h := newAudibilityHarness(t, true)
	h.control.speakerVerdict = func() SpeakerVerdict {
		return SpeakerVerdict{Healthy: true, HasVerdict: true, At: time.Now().Add(-time.Minute)}
	}
	h.control.verdictMaxAge = time.Second
	for range bargeInMinSpeechChunks {
		h.feed(.018, true, false)
	}
	select {
	case req := <-h.watch.requests:
		d := req.Diagnostic
		if d.SpeakerGate != "stale_fail_open" || !d.Audible || !d.Playing || d.NeuralChunks != bargeInMinSpeechChunks || d.DuringArm {
			t.Fatalf("missing actual qualifying evidence: %+v", d)
		}
	case <-h.ctx.Done():
		t.Fatal("expected existing stale-verdict fail-open behavior")
	}
}
