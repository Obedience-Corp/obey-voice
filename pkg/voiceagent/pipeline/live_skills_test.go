package pipeline

import (
	"context"
	"reflect"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
)

type skilledBrain struct {
	brain.Provider
	names []string
}

func (b skilledBrain) RecentActivatedSkills() []string { return b.names }

func TestSpawnedJobCarriesTheLiveBrainsActivatedSkills(t *testing.T) {
	var seen []string
	run := func(ctx context.Context) (string, error) {
		seen = brain.ActivatedSkills(ctx)
		return "", nil
	}
	if _, err := withLiveSkills(skilledBrain{names: []string{"camp-workitems"}}, run)(context.Background()); err != nil {
		t.Fatal(err)
	}
	if want := []string{"camp-workitems"}; !reflect.DeepEqual(seen, want) {
		t.Fatalf("job saw %v, want %v", seen, want)
	}

	seen = []string{"stale"}
	if _, err := withLiveSkills(skilledBrain{}, run)(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 0 {
		t.Fatalf("a job with nothing activated saw %v", seen)
	}
}
