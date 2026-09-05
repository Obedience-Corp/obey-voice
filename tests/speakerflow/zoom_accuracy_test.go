//go:build speakerflow
// +build speakerflow

package speakerflow

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/internal/speaker"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
)

// TestZoomFixtureNamedLabelAccuracy enrolls roster speakers from labeled clips,
// then scores Identify on held-out utterance windows. Fails when the fixture is
// present and accuracy is below 95%.
func TestZoomFixtureNamedLabelAccuracy(t *testing.T) {
	path := zoomFixturePath(t)
	roster := loadZoomRoster(t)

	samples, rate, err := audio.ReadWAVFloat32(path)
	if err != nil {
		t.Fatalf("ReadWAVFloat32: %v", err)
	}
	if rate != fixtureRate {
		t.Fatalf("sample rate = %d, want %d", rate, fixtureRate)
	}

	engine := newLiveEngine(t)
	rev := engine.EmbeddingRev()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	for _, clip := range roster.Enrollment {
		chunk := sliceSamples(samples, clip.StartMS, clip.EndMS)
		if len(chunk) < 8000 {
			t.Fatalf("enrollment clip for %q too short (%d samples)", clip.Speaker, len(chunk))
		}
		emb, err := engine.Embed(ctx, chunk)
		if err != nil {
			t.Fatalf("Embed enrollment %q: %v", clip.Speaker, err)
		}
		if err := engine.SeedEnrolled(clip.Speaker, rev, [][]float32{emb}); err != nil {
			t.Fatalf("SeedEnrolled %q: %v", clip.Speaker, err)
		}
	}
	if engine.EnrolledCount() != len(roster.Enrollment) {
		t.Fatalf("enrolled = %d, want %d", engine.EnrolledCount(), len(roster.Enrollment))
	}

	th := float32(0.55)
	correct, total := 0, 0
	for _, utt := range roster.Utterances {
		if utt.StartMS < roster.LabeledRegion.StartMS || utt.EndMS > roster.LabeledRegion.EndMS {
			continue
		}
		chunk := sliceSamples(samples, utt.StartMS, utt.EndMS)
		if len(chunk) < 8000 {
			t.Logf("skip short utterance %q (%d samples)", utt.Speaker, len(chunk))
			continue
		}
		emb, err := engine.Embed(ctx, chunk)
		if err != nil {
			t.Fatalf("Embed utterance: %v", err)
		}
		label, conf, err := engine.Identify(ctx, emb)
		if err != nil {
			t.Fatalf("Identify: %v", err)
		}
		got := speaker.ApplyThreshold(label, conf, th)
		total++
		if speaker.LabelsEqual(got, utt.Speaker) {
			correct++
		} else {
			t.Logf("miss: want %q got %q (raw %q conf=%.2f)", utt.Speaker, got, label, conf)
		}
	}
	if total == 0 {
		t.Fatal("no scored utterances in labeled region")
	}
	accuracy := float64(correct) / float64(total)
	t.Logf("zoom named-label accuracy: %d/%d = %.1f%%", correct, total, accuracy*100)
	writeAccuracyReport(t, correct, total, accuracy,
		"Harness: enroll roster clips → Identify on held-out windows (production SherpaEngine).")

	if accuracy < zoomAccuracyTarget {
		t.Fatalf("accuracy %.1f%% below 95%% target (%d/%d)", accuracy*100, correct, total)
	}
}

// TestZoomLiveIdentifyDoesNotExplodeSpeakerN runs Identify across many windows
// without enrollment and asserts no speaker-N labels are minted.
func TestZoomLiveIdentifyDoesNotExplodeSpeakerN(t *testing.T) {
	path := zoomFixturePath(t)
	roster := loadZoomRoster(t)

	samples, rate, err := audio.ReadWAVFloat32(path)
	if err != nil {
		t.Fatalf("ReadWAVFloat32: %v", err)
	}
	if rate != fixtureRate {
		t.Fatalf("sample rate = %d, want %d", rate, fixtureRate)
	}

	engine := newLiveEngine(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	distinct := map[string]int{}
	windows := 0
	stepMS := int64(3000)
	for start := roster.LabeledRegion.StartMS; start+stepMS <= roster.LabeledRegion.EndMS; start += stepMS {
		chunk := sliceSamples(samples, start, start+stepMS)
		if len(chunk) < 8000 {
			continue
		}
		emb, err := engine.Embed(ctx, chunk)
		if err != nil {
			t.Fatalf("Embed: %v", err)
		}
		label, _, err := engine.Identify(ctx, emb)
		if err != nil {
			t.Fatalf("Identify: %v", err)
		}
		windows++
		if label != "" && label != speaker.LabelUnknown {
			distinct[label]++
		}
		if strings.HasPrefix(strings.ToLower(label), speaker.LabelSpeakerPrefix) {
			t.Fatalf("Identify minted anonymous label %q on miss path", label)
		}
	}
	if windows == 0 {
		t.Fatal("no windows scored")
	}
	if engine.EnrolledCount() != 0 {
		t.Fatalf("EnrolledCount = %d after unenrolled pass, want 0", engine.EnrolledCount())
	}
	if len(distinct) > len(roster.Speakers) {
		t.Fatalf("distinct live labels = %v (%d) with %d roster speakers — explosion",
			distinct, len(distinct), len(roster.Speakers))
	}
	t.Logf("explosion guard: %d windows, distinct non-unknown labels = %d", windows, len(distinct))
}
