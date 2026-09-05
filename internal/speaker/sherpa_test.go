package speaker

import (
	"context"
	"path/filepath"
	"testing"

	sherpa "github.com/k2-fsa/sherpa-onnx-go/sherpa_onnx"
)

type fakeOfflineDiarizer struct {
	segments []sherpa.OfflineSpeakerDiarizationSegment
}

func (f fakeOfflineDiarizer) Process([]float32) []sherpa.OfflineSpeakerDiarizationSegment {
	return f.segments
}

// TestExpectedLiveRevMatchesEmbeddingRev guards the drift ExpectedLiveRev
// exists to prevent: listing profiles for staleness (ExpectedLiveRev) and
// seeding an engine's own embeddings (EmbeddingRev) must always agree.
func TestExpectedLiveRevMatchesEmbeddingRev(t *testing.T) {
	cases := []Config{
		{},
		{Live: LiveConfig{WindowMS: 1000}},
		{Live: LiveConfig{WindowMS: 1500}},
		{Live: LiveConfig{WindowMS: 3000}},
	}
	for _, cfg := range cases {
		sp := cfg.Normalize()
		engine := &SherpaEngine{windowMS: sp.LiveWindowMS()}
		want := ExpectedLiveRev(sp)
		if got := engine.EmbeddingRev(); got != want {
			t.Fatalf("EmbeddingRev() = %q, want ExpectedLiveRev(%+v) = %q", got, cfg, want)
		}
	}
}

func TestResolveSherpaModelPaths(t *testing.T) {
	models := t.TempDir()
	got := ResolveSherpaModelPaths(Config{}, models)
	if got.Segmentation != filepath.Join(models, defaultSegmentationModel) ||
		got.Embedding != filepath.Join(models, defaultEmbeddingModel) {
		t.Fatalf("default paths = %+v", got)
	}

	abs := filepath.Join(t.TempDir(), "embedding.onnx")
	got = ResolveSherpaModelPaths(Config{Models: ModelsConfig{
		Segmentation: "custom/seg.onnx",
		Embedding:    abs,
	}}, models)
	if got.Segmentation != filepath.Join(models, "custom/seg.onnx") || got.Embedding != abs {
		t.Fatalf("override paths = %+v", got)
	}
}

func TestSherpaEngineMapsNativeSegments(t *testing.T) {
	engine := &SherpaEngine{diarizer: fakeOfflineDiarizer{segments: []sherpa.OfflineSpeakerDiarizationSegment{
		{Start: 0.25, End: 1.5, Speaker: 0},
		{Start: 1.6, End: 3.0, Speaker: 2},
		{Start: 4, End: 4, Speaker: 1}, // invalid span is rejected
	}}}
	timeline, err := engine.Diarize(context.Background(), []float32{0.1}, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(timeline.Observations) != 2 {
		t.Fatalf("observations = %+v", timeline.Observations)
	}
	first, second := timeline.Observations[0], timeline.Observations[1]
	if first.Label != "speaker-1" || first.StartMS != 250 || first.EndMS != 1500 {
		t.Fatalf("first observation = %+v", first)
	}
	if second.Label != "speaker-3" || second.State != StateStable || second.ModelRev == "" {
		t.Fatalf("second observation = %+v", second)
	}
	if err := engine.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Diarize(context.Background(), []float32{1}, 0); err == nil {
		t.Fatal("closed engine should reject diarization")
	}
}

func TestSherpaEngineHonorsCanceledContextBeforeNativeCall(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	engine := &SherpaEngine{diarizer: fakeOfflineDiarizer{}}
	if _, err := engine.Diarize(ctx, []float32{1}, 0); err == nil {
		t.Fatal("expected context cancellation")
	}
}

func TestSherpaIdentifyMissDoesNotRegister(t *testing.T) {
	mgr := sherpa.NewSpeakerEmbeddingManager(4)
	if mgr == nil {
		t.Fatal("could not construct embedding manager")
	}
	engine := &SherpaEngine{manager: mgr, searchThreshold: 0.55, windowMS: 1500}
	defer func() { _ = engine.Close() }()

	misses := [][]float32{{1, 0, 0, 0}, {0, 1, 0, 0}, {0, 0, 1, 0}}
	for i, emb := range misses {
		label, conf, err := engine.Identify(context.Background(), emb)
		if err != nil {
			t.Fatalf("Identify miss %d: %v", i, err)
		}
		if label != LabelUnknown || conf != 0 {
			t.Fatalf("Identify miss %d = (%q, %f), want (%q, 0)", i, label, conf, LabelUnknown)
		}
	}
	if engine.EnrolledCount() != 0 {
		t.Fatalf("EnrolledCount = %d after misses, want 0 (no speaker-1/speaker-2 minted)", engine.EnrolledCount())
	}
}

// TestSeedEnrolledReplacesInPlace runs against the real sherpa embedding
// manager, which needs an embedding dimension rather than a model file and so
// constructs offline. It holds the contract the bug lived in: the manager
// refuses to add a name it already holds, which is exactly what re-enrolling
// the running session's own voice asks it to do.
func TestSeedEnrolledReplacesInPlace(t *testing.T) {
	mgr := sherpa.NewSpeakerEmbeddingManager(4)
	if mgr == nil {
		t.Fatal("could not construct an embedding manager")
	}
	engine := &SherpaEngine{manager: mgr, windowMS: 1500}
	defer func() { _ = engine.Close() }()

	if err := engine.SeedEnrolled("Lance", "titanet-w1500", [][]float32{{1, 0, 0, 0}}); err != nil {
		t.Fatalf("first seed: %v", err)
	}
	// The premise: a plain add of the same name is refused. If sherpa ever
	// starts accepting it, this test says so instead of silently passing.
	if mgr.RegisterV("Lance", [][]float32{{0, 1, 0, 0}}) {
		t.Fatal("sherpa accepted a duplicate name; SeedEnrolled's remove-first step needs revisiting")
	}

	if err := engine.SeedEnrolled("Lance", "titanet-w3000", [][]float32{{0, 1, 0, 0}}); err != nil {
		t.Fatalf("re-enroll seed: %v", err)
	}
	if rev := engine.EnrollRevFor("lance"); rev != "titanet-w3000" {
		t.Fatalf("enrolled rev = %q, want the re-enrolled revision", rev)
	}
	if n := engine.EnrolledCount(); n != 1 {
		t.Fatalf("enrolled count = %d, want 1 — a replace must not double-count", n)
	}
	// The manager resolves the new voiceprint and no longer the replaced one.
	if got := mgr.Search([]float32{0, 1, 0, 0}, 0.5); got != "Lance" {
		t.Fatalf("search for the re-enrolled voice = %q, want Lance", got)
	}
	if got := mgr.Search([]float32{1, 0, 0, 0}, 0.5); got != "" {
		t.Fatalf("search for the replaced voice = %q, want no match", got)
	}
}

// TestSeedEnrolledOnClosedEngine keeps the refusal path honest now that seeding
// mutates the manager before registering.
func TestSeedEnrolledOnClosedEngine(t *testing.T) {
	engine := &SherpaEngine{manager: sherpa.NewSpeakerEmbeddingManager(4)}
	if err := engine.Close(); err != nil {
		t.Fatal(err)
	}
	if err := engine.SeedEnrolled("Lance", "titanet-w1500", [][]float32{{1, 0, 0, 0}}); err == nil {
		t.Fatal("seeding a closed engine must fail")
	}
}
