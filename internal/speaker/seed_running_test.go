package speaker

import (
	"context"
	"strings"
	"testing"
	"time"
)

// nonSeedingEngine is an Engine with no enrollment support, so the analyzer's
// refusal path is exercised without a native model.
type nonSeedingEngine struct{ inner *FakeEngine }

func (e nonSeedingEngine) Embed(ctx context.Context, samples []float32) ([]float32, error) {
	return e.inner.Embed(ctx, samples)
}

func (e nonSeedingEngine) Identify(ctx context.Context, emb []float32) (string, float32, error) {
	return e.inner.Identify(ctx, emb)
}

func (e nonSeedingEngine) Verify(ctx context.Context, name string, emb []float32, th float32) (bool, error) {
	return e.inner.Verify(ctx, name, emb, th)
}

func (e nonSeedingEngine) Diarize(ctx context.Context, samples []float32, n int) (Timeline, error) {
	return e.inner.Diarize(ctx, samples, n)
}

func (e nonSeedingEngine) Close() error { return e.inner.Close() }

// TestAnalyzerSeedEnrolledRefusals covers the two ways a mid-session seed
// cannot happen: an engine that does not enroll, and a closed analyzer.
func TestAnalyzerSeedEnrolledRefusals(t *testing.T) {
	plain, err := NewAnalyzer(liveTestConfig(), nonSeedingEngine{inner: &FakeEngine{}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = plain.Close() }()
	err = plain.SeedEnrolled("lance", "titanet", [][]float32{{0.5}})
	if err == nil || !strings.Contains(err.Error(), "cannot seed") {
		t.Fatalf("seeding a non-enrolling engine = %v, want a cannot-seed error", err)
	}

	closed, err := NewAnalyzer(liveTestConfig(), &FakeEngine{})
	if err != nil {
		t.Fatal(err)
	}
	if err := closed.Close(); err != nil {
		t.Fatal(err)
	}
	if err := closed.SeedEnrolled("lance", "titanet", [][]float32{{0.5}}); err == nil {
		t.Fatal("seeding a closed analyzer must fail")
	}
}

// TestAnalyzerSeedEnrolledLabelsTheNextWindow is the point of the passthrough:
// a voice registered mid-session resolves to its enrolled name without
// rebuilding the engine.
func TestAnalyzerSeedEnrolledLabelsTheNextWindow(t *testing.T) {
	eng := &FakeEngine{Identities: map[string]string{"0.5000": "lance"}, EmbedRev: "titanet-w1500"}
	analyzer, err := NewAnalyzer(liveTestConfig(), eng)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = analyzer.Close() }()

	samples := make([]float32, 100)
	samples[0] = 0.5
	seg := Segment{ID: "s1", End: time.Second, Samples: samples}

	before, err := analyzer.IdentifySegment(context.Background(), seg)
	if err != nil {
		t.Fatal(err)
	}
	if before.EnrollRev != "" {
		t.Fatalf("before seeding, the label must not claim enrollment: %+v", before)
	}

	if err := analyzer.SeedEnrolled("lance", "titanet-w1500", [][]float32{{0.5, 0, 0, 0}}); err != nil {
		t.Fatalf("SeedEnrolled: %v", err)
	}

	after, err := analyzer.IdentifySegment(context.Background(), seg)
	if err != nil {
		t.Fatal(err)
	}
	if after.Label != "lance" || after.EnrollRev != "titanet-w1500" {
		t.Fatalf("after seeding = label %q rev %q, want the enrolled pair", after.Label, after.EnrollRev)
	}
}

// TestLazyLiveSeedEnrolledWithoutEngine: nothing built yet is success, because
// the build that eventually runs seeds every stored profile from disk.
func TestLazyLiveSeedEnrolledWithoutEngine(t *testing.T) {
	lazy := NewLazyLive(context.Background(), liveTestConfig(), newFakeCapture(), func() (Engine, error) {
		return &FakeEngine{}, nil
	}, "")
	defer func() { _ = lazy.Close() }()

	if err := lazy.SeedEnrolled("lance", "titanet", [][]float32{{0.5}}); err != nil {
		t.Fatalf("seeding before the first build = %v, want nil", err)
	}
}

// TestLazyLiveSeedEnrolledReachesBuiltEngine proves the passthrough survives
// the lazy layer: register a voice while labels are on and the running engine
// holds it, no restart.
func TestLazyLiveSeedEnrolledReachesBuiltEngine(t *testing.T) {
	capture := newStubCapture()
	engine := &FakeEngine{}
	lazy := NewLazyLive(context.Background(), liveOnConfig(), capture, func() (Engine, error) {
		return engine, nil
	}, "")
	defer func() { _ = lazy.Close() }()

	lazy.SetEnabled(true)
	waitForBuild(t, capture)

	if err := lazy.SeedEnrolled("lance", "titanet", [][]float32{{0.5, 0, 0, 0}}); err != nil {
		t.Fatalf("SeedEnrolled: %v", err)
	}
	if rev := engine.EnrollRevFor("lance"); rev != "titanet" {
		t.Fatalf("engine enrolled rev = %q, want titanet", rev)
	}
}
