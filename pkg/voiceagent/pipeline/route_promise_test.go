package pipeline

import "testing"

func TestIsToolPromiseJarvisRegression(t *testing.T) {
	t.Parallel()
	positives := []string{
		"I am invoking the tool now",
		"I shall execute the specific command",
		"I am composing the precise command sequence",
		"I shall filter the work items and invoke the tool now to retrieve only those records.",
	}
	for _, s := range positives {
		if !isToolPromise(s) {
			t.Errorf("isToolPromise(%q) = false, want true", s)
		}
	}
}

func TestIsToolPromiseNegatives(t *testing.T) {
	t.Parallel()
	negatives := []string{
		"I shall have a think about that.",
		"Hello, how are you?",
		"The weather looks nice today.",
		"I will think about your question.",
	}
	for _, s := range negatives {
		if isToolPromise(s) {
			t.Errorf("isToolPromise(%q) = true, want false", s)
		}
	}
}

func TestIsToolPromiseSkipsWhenToolsFired(t *testing.T) {
	t.Parallel()
	repair := &promiseRepairState{enabled: true, toolsFired: true}
	if repair.shouldRepair("I am invoking the tool now") {
		t.Fatal("repair must not run when tools already fired")
	}
}

func TestRouteKindFromBrain(t *testing.T) {
	t.Parallel()
	if got := routeKindFromBrain("spawn"); got != RouteSpawn {
		t.Fatalf("spawn = %q", got)
	}
	if got := routeKindFromBrain("narrate"); got != RouteNarrate {
		t.Fatalf("narrate = %q", got)
	}
	if got := routeKindFromBrain("chat"); got != RouteChat {
		t.Fatalf("chat = %q", got)
	}
}
