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

// TestZoomFixtureNamedLabelAccuracy enrolls roster speakers from labeled clips,
// then scores Identify on held-out utterance windows. Fails when the fixture is
// present and accuracy is below 95%.
func TestZoomFixtureNamedLabelAccuracy(t *testing.T) {
	// The dedicated Zoom tutorial clip is one narrator describing two tracks
	// (diarize finds a single cluster). Named-label accuracy needs a real
	// multi-voice recording: the 90s product-marketing meeting fixture.
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
	diarizer, err := speaker.NewSherpaEngine(cfg, config.ModelsDirFrom(appCfg))
	if err != nil {
		t.Fatalf("NewSherpaEngine: %v", err)
	}
	defer func() { _ = diarizer.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	timeline, err := diarizer.Diarize(ctx, samples, 2)
	if err != nil {
		t.Fatalf("Diarize: %v", err)
	}

	byLabel := map[string][]speaker.Observation{}
	for _, obs := range timeline.Observations {
		if obs.Label == "" || obs.Label == speaker.LabelUnknown {
			continue
		}
		if obs.EndMS-obs.StartMS < 800 {
			continue
		}
		byLabel[obs.Label] = append(byLabel[obs.Label], obs)
	}
	if len(byLabel) < 2 {
		t.Fatalf("diarize found %d usable speakers, want >= 2: %+v", len(byLabel), byLabel)
	}

	live := diarizer
	rev := live.EmbeddingRev()
	type namedSpan struct {
		name string
		obs  speaker.Observation
	}
	var heldOut []namedSpan
	enrolled := 0
	for label, spans := range byLabel {
		name := "zoom-" + label
		enroll := spans[0]
		enrollEnd := enroll.StartMS + 1500
		if enrollEnd > enroll.EndMS {
			enrollEnd = enroll.EndMS
		}
		chunk := sliceSamples(samples, enroll.StartMS, enrollEnd)
		if len(chunk) < 8000 {
			t.Logf("skip enroll %s: too short", name)
			continue
		}
		emb, err := live.Embed(ctx, chunk)
		if err != nil {
			t.Fatalf("Embed enroll %s: %v", name, err)
		}
		if err := live.SeedEnrolled(name, rev, [][]float32{emb}); err != nil {
			t.Fatalf("SeedEnrolled %s: %v", name, err)
		}
		self, selfConf, err := live.Identify(ctx, emb)
		if err != nil {
			t.Fatalf("Identify enroll clip %s: %v", name, err)
		}
		t.Logf("self-identify %s -> %q conf=%.2f enrolled=%d", name, self, selfConf, live.EnrolledCount())
		enrolled++
		for i, obs := range spans {
			if i == 0 {
				restStart := enrollEnd
				if obs.EndMS-restStart >= 800 {
					heldOut = append(heldOut, namedSpan{name: name, obs: speaker.Observation{StartMS: restStart, EndMS: obs.EndMS}})
				}
				continue
			}
			heldOut = append(heldOut, namedSpan{name: name, obs: obs})
		}
	}
	if enrolled < 2 {
		t.Fatalf("enrolled %d speakers, want 2", enrolled)
	}

	th := float32(0.55)
	const windowMS int64 = 1500
	correct, total := 0, 0
	for _, u := range heldOut {
		for start := u.obs.StartMS; start+windowMS <= u.obs.EndMS; start += windowMS {
			chunk := sliceSamples(samples, start, start+windowMS)
			if len(chunk) < 8000 {
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
			if speaker.LabelsEqual(got, u.name) {
				correct++
			} else {
				t.Logf("miss: want %q got %q (raw %q conf=%.2f) %d-%dms", u.name, got, label, conf, start, start+windowMS)
			}
		}
	}
	if total == 0 {
		t.Fatal("no scored held-out spans")
	}
	accuracy := float64(correct) / float64(total)
	t.Logf("zoom named-label accuracy: %d/%d = %.1f%%", correct, total, accuracy*100)
	writeAccuracyReport(t, correct, total, accuracy,
		"Harness: diarize Zoom clip → enroll first 1.5s of each cluster → Identify held-out spans.")

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
