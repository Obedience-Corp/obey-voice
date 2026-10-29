package pipeline

import "testing"

func TestIsToolPromiseJarvisRegression(t *testing.T) {
	t.Parallel()
	positives := []string{
		"I am invoking the tool now",
		"I shall execute the specific command",
		"I am composing the precise command sequence",
		"I shall filter the work items and invoke the tool now to retrieve only those records.",
		"I'll run the command now.",
		"I’m going to use the tool to check the files.",
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
		"Your skills, your tools, your knowledge — they all help.",
		"You're more than just a tool.",
		"This command prints the current directory.",
		"The execution was successful.",
		"I will explain how the tool works.",
		"I will not run that command.",
		"I already ran the command.",
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
