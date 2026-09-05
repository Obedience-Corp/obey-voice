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
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

// TestZoomRosterIdentifyDiagnostic scores Identify against the checked-in Zoom
// roster on zoom-call.wav. It is not a 95% gate: the current source is a
// single-narrator tutorial, and this PR does not claim named-speaker accuracy.
// Inadequate coverage (no scorable roster windows) fails. The explosion test
// below is the Zoom-lane gate.
func TestZoomRosterIdentifyDiagnostic(t *testing.T) {
	path := zoomFixturePath(t)
	roster := loadZoomRoster(t)

	samples, rate, err := audio.ReadWAVFloat32(path)
	if err != nil {
		t.Fatalf("ReadWAVFloat32: %v", err)
	}
	if rate != fixtureRate {
		t.Fatalf("sample rate = %d, want %d", rate, fixtureRate)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	live := newLiveEngine(t)
	rev := live.EmbeddingRev()
	for _, clip := range roster.Enrollment {
		chunk := sliceSamples(samples, clip.StartMS, clip.EndMS)
		if len(chunk) < 8000 {
			t.Fatalf("enrollment clip for %q too short (%d samples)", clip.Speaker, len(chunk))
		}
		emb, err := live.Embed(ctx, chunk)
		if err != nil {
			t.Fatalf("Embed enrollment %q: %v", clip.Speaker, err)
		}
		if err := live.SeedEnrolled(clip.Speaker, rev, [][]float32{emb}); err != nil {
			t.Fatalf("SeedEnrolled %q: %v", clip.Speaker, err)
		}
	}
	if live.EnrolledCount() != len(roster.Enrollment) {
		t.Fatalf("enrolled = %d, want %d", live.EnrolledCount(), len(roster.Enrollment))
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
		emb, err := live.Embed(ctx, chunk)
		if err != nil {
			t.Fatalf("Embed utterance: %v", err)
		}
		label, conf, err := live.Identify(ctx, emb)
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
	t.Logf("zoom roster Identify diagnostic (not a 95%% gate) %s: %d/%d = %.1f%%", roster.SourceURL, correct, total, accuracy*100)
}

// TestMeetingFixtureIdentifyHeldOutWindows is a diagnostic on the 90s
// product-marketing meeting clip. It enrolls from diarize clusters and scores
// later 1.5s windows. Cluster ids are not speaker names. This does not gate
// speakerflow and does not report Zoom accuracy.
func TestMeetingFixtureIdentifyHeldOutWindows(t *testing.T) {
	path := fixturePath(t)
	samples, rate, err := audio.ReadWAVFloat32(path)
	if err != nil {
		t.Fatalf("ReadWAVFloat32: %v", err)
	}
	if rate != fixtureRate {
		t.Fatalf("sample rate = %d, want %d", rate, fixtureRate)
	}

	appCfg := &config.Config{ModelsDir: config.ModelsDir()}
	appCfg.Speaker.Enabled = true
	appCfg.Speaker.Meeting.Enabled = true
	appCfg.Speaker.Meeting.NumSpeakers = 2
	if err := config.EnsureRuntimeAssets(t.Context(), appCfg, config.AssetRequest{NeedSpeaker: true}, nil); err != nil {
		t.Fatalf("EnsureRuntimeAssets: %v", err)
	}
	cfg := speaker.FromAppConfig(appCfg)
	engine, err := speaker.NewSherpaEngine(cfg, config.ModelsDirFrom(appCfg))
	if err != nil {
		t.Fatalf("NewSherpaEngine: %v", err)
	}
	defer func() { _ = engine.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	timeline, err := engine.Diarize(ctx, samples, 2)
	if err != nil {
		t.Fatalf("Diarize: %v", err)
	}

	byLabel := map[string][]speaker.Observation{}
	for _, obs := range timeline.Observations {
		if obs.Label == "" || obs.Label == speaker.LabelUnknown || obs.EndMS-obs.StartMS < 800 {
			continue
		}
		byLabel[obs.Label] = append(byLabel[obs.Label], obs)
	}
	if len(byLabel) < 2 {
		t.Skipf("meeting fixture diarize found %d clusters, want >= 2", len(byLabel))
	}

	rev := engine.EmbeddingRev()
	type clusterSpan struct {
		cluster string
		startMS int64
		endMS   int64
	}
	var heldOut []clusterSpan
	for cluster, spans := range byLabel {
		enroll := spans[0]
		enrollEnd := enroll.StartMS + 1500
		if enrollEnd > enroll.EndMS {
			enrollEnd = enroll.EndMS
		}
		chunk := sliceSamples(samples, enroll.StartMS, enrollEnd)
		if len(chunk) < 8000 {
			continue
		}
		emb, err := engine.Embed(ctx, chunk)
		if err != nil {
			t.Fatalf("Embed enroll %s: %v", cluster, err)
		}
		if err := engine.SeedEnrolled(cluster, rev, [][]float32{emb}); err != nil {
			t.Fatalf("SeedEnrolled %s: %v", cluster, err)
		}
		for i, obs := range spans {
			start := obs.StartMS
			if i == 0 {
				start = enrollEnd
			}
			if obs.EndMS-start >= 1500 {
				heldOut = append(heldOut, clusterSpan{cluster: cluster, startMS: start, endMS: obs.EndMS})
			}
		}
	}

	const windowMS int64 = 1500
	th := float32(0.55)
	correct, total := 0, 0
	for _, u := range heldOut {
		for start := u.startMS; start+windowMS <= u.endMS; start += windowMS {
			chunk := sliceSamples(samples, start, start+windowMS)
			if len(chunk) < 8000 {
				continue
			}
			emb, err := engine.Embed(ctx, chunk)
			if err != nil {
				t.Fatalf("Embed: %v", err)
			}
			label, conf, err := engine.Identify(ctx, emb)
			if err != nil {
				t.Fatalf("Identify: %v", err)
			}
			got := speaker.ApplyThreshold(label, conf, th)
			total++
			if speaker.LabelsEqual(got, u.cluster) {
				correct++
			}
		}
	}
	if total == 0 {
		t.Fatal("no diagnostic windows")
	}
	t.Logf("meeting-fixture Identify diagnostic (diarize clusters, not names): %d/%d = %.1f%%",
		correct, total, 100*float64(correct)/float64(total))
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
	regionStart, regionEnd := roster.LabeledRegion.StartMS, roster.LabeledRegion.EndMS
	if regionEnd <= regionStart {
		regionStart, regionEnd = 0, int64(len(samples))*1000/int64(rate)
	}
	for start := regionStart; start+stepMS <= regionEnd; start += stepMS {
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
	t.Logf("explosion guard: %d windows, distinct non-unknown labels = %d (bound %d roster speakers)",
		windows, len(distinct), len(roster.Speakers))
}
