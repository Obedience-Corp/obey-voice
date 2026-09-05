package pipeline

import (
	"testing"
)

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
