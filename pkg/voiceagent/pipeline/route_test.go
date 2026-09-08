package pipeline

import (
	"testing"
)

func TestClassifySpawnIntent(t *testing.T) {
	t.Parallel()
	spawns := []string{
		"search the web for rust 1.90",
		"look up the latest kernel CVE",
		"create a file called notes.md",
		"run this command",
		"filter design work items",
	}
	for _, s := range spawns {
		kind, source, ok := classifyTurn(s, RouteContext{})
		if !ok || kind != RouteSpawn || source != "intent" {
			t.Errorf("classifyTurn(%q) = %q %q %v, want spawn/intent", s, kind, source, ok)
		}
	}
	chats := []string{
		"how's it going",
		"what's the capital of Australia",
		"tell me a joke",
		"search your feelings",
	}
	for _, s := range chats {
		if classifySpawnIntent(s) {
			t.Errorf("classifySpawnIntent(%q) = true, want chat", s)
		}
	}
}

func TestClassifyPhraseNarrateExamples(t *testing.T) {
	t.Parallel()
	ctx := RouteContext{}
	cases := []struct {
		utterance string
		want      RouteKind
	}{
		{"Tell me a long story", RouteNarrate},
		{"READ THIS", RouteNarrate},
		{"read the article", RouteNarrate},
		{"read chapter", RouteNarrate},
		{"audiobook", RouteNarrate},
		{"play the book", RouteNarrate},
	}
	for _, tc := range cases {
		t.Run(tc.utterance, func(t *testing.T) {
			got, ok := classifyPhrase(tc.utterance, ctx)
			if !ok || got != tc.want {
				t.Fatalf("classifyPhrase(%q) = %q, %v; want %q, true", tc.utterance, got, ok, tc.want)
			}
		})
	}
}

func TestClassifyPhraseNarrationControlsRequireMode(t *testing.T) {
	t.Parallel()
	inactive := RouteContext{NarrationActive: false}
	active := RouteContext{NarrationActive: true}

	for _, phrase := range []string{"pause", "hold on", "wait", "keep going", "continue", "resume", "skip", "next section", "stop reading"} {
		if kind, ok := classifyPhrase(phrase, inactive); ok {
			t.Errorf("phrase %q matched %q without narration; want fall through", phrase, kind)
		}
	}

	checks := map[string]RouteKind{
		"pause":         RouteNarratePause,
		"resume":        RouteNarrateResume,
		"skip":          RouteNarrateSkip,
		"stop reading":  RouteNarrateStop,
		"that's enough": RouteNarrateStop,
	}
	for phrase, want := range checks {
		got, ok := classifyPhrase(phrase, active)
		if !ok || got != want {
			t.Fatalf("active %q = %q, %v; want %q, true", phrase, got, ok, want)
		}
	}
}

func TestClassifyPhraseJobControlsRequireJob(t *testing.T) {
	t.Parallel()
	noJob := RouteContext{JobActive: false}
	withJob := RouteContext{JobActive: true}

	for _, phrase := range []string{"abort", "cancel that", "never mind", "status", "are you done"} {
		if kind, ok := classifyPhrase(phrase, noJob); ok {
			t.Errorf("phrase %q matched %q without job; want fall through to chat", phrase, kind)
		}
	}

	got, ok := classifyPhrase("abort", withJob)
	if !ok || got != RouteJobAbort {
		t.Fatalf("abort with job = %q, %v; want job.abort, true", got, ok)
	}
	got, ok = classifyPhrase("status", withJob)
	if !ok || got != RouteJobStatus {
		t.Fatalf("status with job = %q, %v; want job.status, true", got, ok)
	}
}

func TestClassifyPhraseCancelVerbsNeedAJob(t *testing.T) {
	t.Parallel()
	withJob := RouteContext{JobActive: true}
	for _, phrase := range []string{"kill it", "kill that", "cancel", "cancel it", "Kill it.", "Cancel that!"} {
		kind, ok := classifyPhrase(phrase, withJob)
		if !ok || kind != RouteJobAbort {
			t.Errorf("%q with job = %q, %v; want job.abort, true", phrase, kind, ok)
		}
		if kind, ok := classifyPhrase(phrase, RouteContext{}); ok {
			t.Errorf("%q without job = %q, true; want fall through to chat", phrase, kind)
		}
	}
	// "stop" on its own is speech's verb, never the job's.
	if kind, ok := classifyPhrase("stop", withJob); ok {
		t.Errorf("stop with job = %q, true; want no phrase match", kind)
	}
}

func TestClassifyPhraseIgnoresEndPunctuation(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   string
		ctx  RouteContext
		want RouteKind
	}{
		{"What's happening?", RouteContext{JobActive: true}, RouteJobStatus},
		{"Are you done?", RouteContext{JobActive: true}, RouteJobStatus},
		{"Pause.", RouteContext{NarrationActive: true}, RouteNarratePause},
		{"never mind...", RouteContext{JobActive: true}, RouteJobAbort},
	}
	for _, tc := range cases {
		kind, ok := classifyPhrase(tc.in, tc.ctx)
		if !ok || kind != tc.want {
			t.Errorf("%q = %q, %v; want %q, true", tc.in, kind, ok, tc.want)
		}
	}
	if !classifySpawnIntent("Google the weather.") {
		t.Error("a trailing period must not hide a spawn intent")
	}
}

func TestClassifyPhraseNonMatch(t *testing.T) {
	t.Parallel()
	if kind, ok := classifyPhrase("how's the weather", RouteContext{}); ok {
		t.Fatalf("weather = %q, true; want no match", kind)
	}
}

func TestClassifyPhraseStopReadingBeatsShorterOverlap(t *testing.T) {
	t.Parallel()
	active := RouteContext{NarrationActive: true}
	got, ok := classifyPhrase("stop reading", active)
	if !ok || got != RouteNarrateStop {
		t.Fatalf("stop reading = %q, %v; want narrate.stop", got, ok)
	}
}

func TestClassifyPhraseEmptyNoOp(t *testing.T) {
	t.Parallel()
	if kind, ok := classifyPhrase("   ", RouteContext{}); ok {
		t.Fatalf("empty = %q, true; want no match", kind)
	}
}
