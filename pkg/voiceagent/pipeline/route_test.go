package pipeline

import (
	"slices"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/chatmedia"
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
		if classifyWorkIntent(s, RouteContext{}) {
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
	if !classifyWorkIntent("Google the weather.", RouteContext{}) {
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

func TestPictureDeliveryRequiresLegacyWorkerAndExplicitRequest(t *testing.T) {
	const request = "Please show me an image of a monkey."
	for _, ctx := range []RouteContext{{}, {TieredWorkers: true, LegacyImageWorker: true}} {
		if kind, _, _ := classifyTurn(request, ctx); kind == RouteSpawn {
			t.Fatalf("unexpected image dispatch with %+v", ctx)
		}
	}
	ctx := RouteContext{LegacyImageWorker: true}
	for _, input := range []string{"You just showed me a cat.", "Can you show me what the monkey in this picture is holding?", "What does this picture of a monkey mean?", "Don't show me a picture of a monkey."} {
		if kind, _, _ := classifyTurn(input, ctx); kind == RouteSpawn {
			t.Errorf("unexpected image dispatch for %q", input)
		}
	}
}

func TestPictureDeliveryFollowups(t *testing.T) {
	history := []brain.Turn{{Role: "user", Content: "Can you show me a picture of a monkey?"}, {Role: "assistant", Content: "I can't fetch a monkey picture directly in this chat."}}
	for _, tc := range []struct {
		input string
		want  bool
	}{
		{"Yeah, so show me a monkey.", true},
		{"You need to add them to the chat, please.", true},
		{"Can you show me a command?", false},
		{"Don't add them to the chat.", false},
		{"Why can't you add them to the chat?", false},
	} {
		if got := requestsChatImage(tc.input, history); got != tc.want {
			t.Errorf("%q = %v", tc.input, got)
		}
	}
}

func TestDirectWorkIntentUsesAnAvailableBackgroundWorkerForLongTasks(t *testing.T) {
	for _, tc := range []struct {
		input         string
		worker, spawn bool
	}{
		{"Build this project.", false, false},
		{"Build this project.", true, true},
		{"Implement a command.", true, true},
		{"Run this command.", true, false},
		{"What should we build this week?", true, false},
	} {
		kind, _, _ := classifyTurn(tc.input, RouteContext{DirectTools: true, LegacyImageWorker: tc.worker})
		if (kind == RouteSpawn) != tc.spawn {
			t.Errorf("%q worker=%v: route=%v, want spawn=%v", tc.input, tc.worker, kind, tc.spawn)
		}
	}
}

func TestCompoundTaskIsAnImageOnlyWhileAPictureIsOwed(t *testing.T) {
	// The turn order and wording follow a recorded conversation. Only the
	// subject is changed.
	asked := []brain.Turn{
		{Role: "user", Content: "No, I mean like show me a picture of some dogs from the internet."},
		{Role: "assistant", Content: "Alright, let me find something nice for you."},
		{Role: "user", Content: "I'll do it."},
		{Role: "assistant", Content: "Just wait."},
	}
	delivered := []brain.Turn{
		{Role: "user", Content: "Show me a picture of a dog."},
		{Role: "assistant", Content: "Here it is.", Attachments: []chatmedia.Ref{{ID: "delivered"}}},
	}
	mentioned := []brain.Turn{
		{Role: "user", Content: "What's a good caption for my vacation photo?"},
		{Role: "assistant", Content: "Try: sunset over the bay."},
	}
	then := func(history []brain.Turn, lines ...string) []brain.Turn {
		history = slices.Clone(history)
		for _, line := range lines {
			history = append(history, brain.Turn{Role: "user", Content: line})
		}
		return history
	}
	refused := []brain.Turn{
		{Role: "user", Content: "Show me a picture of the Eiffel Tower."},
		{Role: "assistant", Content: "I can't browse for images right now."},
	}
	const find = "You need to search the internet find a nice pair of dogs and add it to the chat"
	const weather = "search the web for today's weather and add it to the chat"
	for _, tc := range []struct {
		name    string
		history []brain.Turn
		task    string
		want    bool
	}{
		{"the picture was asked for and never arrived", asked, find, true},
		{"the task is already in history", then(asked, find), find, true},
		{"the task points back by pronoun", asked, "You need to search the internet and find one and add it to the chat", true},
		{"the task asks for the picture itself", nil, "find a picture of a dog and add it to the chat", true},
		{"a second turn about something else ends the request", then(asked, "Write a shopping list for dinner."), find, false},
		{"the request was withdrawn", then(asked, "Never mind."), find, false},
		{"the last picture was delivered", delivered, weather, false},
		{"the last picture was delivered", delivered, "look up today's weather and add it to the chat", false},
		{"the last picture was delivered", delivered, "look up the capital of France and put it in the chat", false},
		{"delivered, then an assent", then(delivered, "ok"), weather, false},
		{"a picture was only mentioned", mentioned, "search the web for flight prices and add them to the chat", false},
		{"the task only mentions a picture", nil, "write a caption for my vacation photo and add it to the chat", false},
		{"the task only mentions a picture", nil, "write alt text for the image and put it in the chat", false},
		{"the task only mentions a picture", nil, "list the pictures in my downloads folder and add them to the chat", false},
		{"no conversation", nil, weather, false},
		{"a withdrawal is not a task", asked, "search the internet and don't add it to the chat", false},
		{"another lookup while the picture is owed", asked, weather, false},
		{"another lookup after a refusal", refused, "find the population of paris and put it in the chat", false},
		// Only the shared subject ties this task to the owed picture, so a
		// different lookup about the same subject is held to an image too.
		{"another lookup about the same subject", asked, "search the web for prices of dogs and add it to the chat", true},
	} {
		if got := requestsChatImage(tc.task, tc.history); got != tc.want {
			t.Errorf("%s: %q = %t", tc.name, tc.task, got)
		}
	}
}
