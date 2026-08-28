package meeting

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/internal/listen"
	meetinglog "github.com/Obedience-Corp/obey-voice/internal/meeting/log"
	"github.com/Obedience-Corp/obey-voice/internal/speaker"
)

// blockingSpeakerAnalyzer stands in for a slow review pass — the sherpa
// diarizer on a long meeting, or the Sortformer CLI subprocess. It blocks until
// released or until the caller's context is cancelled, which is the Ctrl+C
// path.
type blockingSpeakerAnalyzer struct {
	started chan struct{}
	release chan struct{}
	samples int
}

func newBlockingSpeakerAnalyzer() *blockingSpeakerAnalyzer {
	return &blockingSpeakerAnalyzer{started: make(chan struct{}), release: make(chan struct{})}
}

func (a *blockingSpeakerAnalyzer) Finalize(ctx context.Context, samples []float32) (speaker.Timeline, error) {
	a.samples = len(samples)
	close(a.started)
	select {
	case <-a.release:
		return speaker.Timeline{Observations: []speaker.Observation{{
			SegmentID: "diarization-1", StartMS: 0, EndMS: 1000,
			Label: "speaker-1", State: speaker.StateStable, Source: speaker.SourceRecording,
		}}}, nil
	case <-ctx.Done():
		return speaker.Timeline{}, ctx.Err()
	}
}

func newBackgroundSession(t *testing.T, analyzer SpeakerAnalyzer) (*SpeakerSession, *fakeCaptureSubscriber, *meetinglog.Writer) {
	t.Helper()
	bundlePath := filepath.Join(t.TempDir(), "planning.meeting")
	writer, err := meetinglog.CreateBundle(bundlePath, "Planning", "fake", "")
	if err != nil {
		t.Fatal(err)
	}
	capture := &fakeCaptureSubscriber{}
	session, err := NewSpeakerSession(capture, analyzer, writer, bundlePath, true)
	if err != nil {
		t.Fatal(err)
	}
	return session, capture, writer
}

// Diarization runs after capture, never during it: by the time the analyzer is
// working, the mic subscription is released and the streamed WAV is closed.
func TestBackgroundDiarizationDoesNotHoldCapture(t *testing.T) {
	analyzer := newBlockingSpeakerAnalyzer()
	session, capture, _ := newBackgroundSession(t, analyzer)
	capture.publish([]float32{0.1, 0.2})
	capture.publish([]float32{0.3})

	done := make(chan AnalysisResult, 1)
	go func() {
		result, _ := session.Finalize(context.Background())
		done <- result
	}()

	select {
	case <-analyzer.started:
	case <-time.After(10 * time.Second):
		t.Fatal("Finalize never reached the analyzer")
	}
	if !capture.released() {
		t.Error("the PCM subscription is still held while diarization runs")
	}
	if analyzer.samples != 3 {
		t.Errorf("diarized %d samples, want the 3 captured before stop", analyzer.samples)
	}

	close(analyzer.release)
	select {
	case result := <-done:
		if result.Status != AnalysisComplete {
			t.Fatalf("result = %+v, want a completed analysis", result)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Finalize did not return after the analyzer was released")
	}
}

// Ctrl+C during background review cancels the labels only. The recording, the
// bundle and the analysis sidecar all survive, and the failure is reported
// rather than swallowed.
func TestBackgroundDiarizationSkipsOnCancel(t *testing.T) {
	analyzer := newBlockingSpeakerAnalyzer()
	session, capture, writer := newBackgroundSession(t, analyzer)
	if err := writer.OnUtterance(listen.Utterance{
		Text: "the transcript survives", At: writer.StartedAt().Add(3 * time.Second),
	}); err != nil {
		t.Fatal(err)
	}
	capture.publish([]float32{0.1, 0.2})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan AnalysisResult, 1)
	go func() {
		result, _ := session.Finalize(ctx)
		done <- result
	}()

	select {
	case <-analyzer.started:
	case <-time.After(10 * time.Second):
		t.Fatal("Finalize never reached the analyzer")
	}
	cancel()

	var result AnalysisResult
	select {
	case result = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Finalize did not return after the context was cancelled")
	}
	if result.Status != AnalysisError || !strings.Contains(result.Error, context.Canceled.Error()) {
		t.Fatalf("result = %+v, want a cancelled analysis reported as an error", result)
	}
	if _, err := os.Stat(result.Artifact); err != nil {
		t.Errorf("analysis sidecar missing after a skip: %v", err)
	}
	if _, err := os.Stat(result.AudioFile); err != nil {
		t.Errorf("meeting audio missing after a skip: %v", err)
	}

	if _, err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	log, err := os.ReadFile(writer.Path())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(log), "the transcript survives") {
		t.Errorf("the transcript did not survive a skipped diarization:\n%s", log)
	}
}
