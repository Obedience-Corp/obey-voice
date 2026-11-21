package pipeline

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/internal/testmedia"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/chatmedia"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/job"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/session"
	"github.com/ollama/ollama/api"
)

func imageWorkerFixture(t *testing.T, expectInputImage bool) (*config.Config, *brain.OllamaBrain, chatmedia.Ref, string, <-chan struct{}, chan<- struct{}) {
	t.Helper()
	root := t.TempDir()
	config.SetConfigDirForTest(t, filepath.Join(root, "config"))
	ref, err := chatmedia.DefaultStore().Import("input.png", testmedia.PNG())
	if err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(root, "result.gif")
	if err = os.WriteFile(output, testmedia.GIF(), 0600); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tags":
			_ = json.NewEncoder(w).Encode(map[string]any{"models": []map[string]string{{"name": "fixture"}}})
		case "/api/show":
			_ = json.NewEncoder(w).Encode(map[string]any{"capabilities": []string{"completion", "vision", "tools"}})
		case "/api/chat":
			var req api.ChatRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Error(err)
				return
			}
			found := false
			for _, m := range req.Messages {
				for _, image := range m.Images {
					if bytes.Equal(image, testmedia.PNG()) {
						found = true
					}
				}
			}
			if found != expectInputImage {
				t.Errorf("worker received input image = %v, want %v", found, expectInputImage)
			}
			started <- struct{}{}
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
			_ = json.NewEncoder(w).Encode(api.ChatResponse{Model: "fixture", Message: api.Message{Role: "assistant", Content: "I inspected the two blocks.\n![Animated result](<" + output + ">)"}, Done: true})
		default:
			t.Errorf("unexpected endpoint: %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	cfg := &config.Config{ContinuityIntegrityEnabled: true, ContinuityRecoveryMax: 2, MaxHistory: 20, OllamaHost: server.URL, OllamaModel: "fixture", VoiceWorkerProvider: "ollama", VoiceToolsEnabled: true, WorkDir: root, PromptsDir: filepath.Join(root, "prompts"), AgentName: "Obey"}
	worker, err := brain.NewOllama(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return cfg, worker, ref, output, started, release
}
func TestWorkerImagesSurviveAdmissionAndPersistReply(t *testing.T) {
	cfg, worker, ref, output, started, release := imageWorkerFixture(t, true)
	p := &Pipeline{Brain: worker, Events: events.NewBus()}
	if err := p.BeginConversationIdentity("images"); err != nil {
		t.Fatal(err)
	}
	jobs := job.NewTable(t.Context())
	defer jobs.Close()
	a := NewAgency(p, jobs, worker, cfg, nil)
	a.Wire()
	p.incActiveTurn()
	defer p.decActiveTurn()
	done := make(chan events.JobFinished, 1)
	events.Subscribe(p.Events, func(e events.JobFinished) { done <- e })
	ctx, cancel := context.WithCancel(chatmedia.WithInput(t.Context(), []chatmedia.Ref{ref}))
	defer cancel()
	metrics := newTurnMetrics()
	a.handleSpawn(ctx, p.newTurnConductor(metrics), metrics, RouteDecision{Kind: RouteSpawn, Transcript: "Inspect the image and return the animation"})
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("worker never started")
	}
	cancel()
	close(release) // Foreground cancellation must not discard admitted work.
	var result events.JobFinished
	select {
	case result = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("worker never finished")
	}
	if len(result.Attachments) != 1 || strings.Contains(result.Summary, "![") {
		t.Fatalf("result %+v", result)
	}
	store := session.NewStore(filepath.Join(t.TempDir(), "sessions"))
	saved := session.New("ollama", "fixture")
	p.OnTurn = func() {
		if err := store.Save(saved, p.Brain.History()); err != nil {
			t.Error(err)
		}
	}
	if err := p.commitWorkerReplies(); err != nil {
		t.Fatal(err)
	}
	turns := p.Brain.History()
	last := turns[len(turns)-1]
	if len(last.Attachments) != 1 || last.Attachments[0] != result.Attachments[0] {
		t.Fatalf("history %+v", turns)
	}
	items := p.Workers()
	if len(items) != 1 || len(items[0].Thread[0].Attachments) != 1 || len(items[0].Thread[len(items[0].Thread)-1].Attachments) != 1 {
		t.Fatalf("thread %+v", items)
	}
	if err := os.Remove(output); err != nil {
		t.Fatal(err)
	}
	if _, err := chatmedia.DefaultStore().Preview(last.Attachments[0]); err != nil {
		t.Fatal("returned asset did not survive source removal:", err)
	}
	restored, err := store.Load(saved.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(restored.Turns[len(restored.Turns)-1].Attachments) != 1 {
		t.Fatal("saved reply lost images")
	}
	if err := p.ClearConversation(); err != nil {
		t.Fatal(err)
	}
	if len(p.Workers()) != 0 {
		t.Fatal("image worker crossed conversation boundary")
	}
}
func TestForegroundImageReplyBecomesGroupCompletion(t *testing.T) {
	_, provider, ref, _, _, release := imageWorkerFixture(t, true)
	close(release)
	player := newFakePlayer(time.Millisecond)
	defer player.Close()
	p := &Pipeline{Brain: provider, TTS: &fakeTTS{}, Player: player, Events: events.NewBus()}
	var image events.ReplyImagesReady
	events.Subscribe(p.Events, func(e events.ReplyImagesReady) { image = e })
	spoke := false
	events.Subscribe(p.Events, func(events.GeneratingVoice) {
		spoke = true
		if len(image.Attachments) != 1 {
			t.Error("typed reply began synthesis before delivering its image")
		}
	})
	var response events.ResponseReady
	events.Subscribe(p.Events, func(e events.ResponseReady) { response = e })
	state := &completionState{}
	ctx := withCompletionState(chatmedia.WithInput(t.Context(), []chatmedia.Ref{ref}), state)
	if err := p.RunTurnTextMode(ctx, "Inspect and return the animation"); err != nil {
		t.Fatal(err)
	}
	if !spoke || len(response.Attachments) != 1 || len(image.Attachments) != 1 || image.Attachments[0] != response.Attachments[0] || strings.Contains(response.Response, "![") {
		t.Fatalf("response %+v", response)
	}
	// The group completion consumes the same durable reply, not a second import.
	metrics := newTurnMetrics()
	// The actual turn already attached media; test the turn completion path with its finalization state.
	metrics.mediaReply = &response
	p.completeAgentResponse(ctx, response.Response, metrics, false)
	if !state.ok || len(state.turn.Attachments) != 1 || state.turn.Content != response.Response {
		t.Fatalf("group completion %+v", state)
	}
}
func TestUnsupportedWorkerImagesAndSpeechSuppression(t *testing.T) {
	if err := validateProviderImages(t.Context(), &slowWorker{}, []chatmedia.Ref{{ID: "image"}}); err == nil {
		t.Fatal("unsupported worker accepted images")
	}
	projection := NewSpeechProjection()
	for _, chunk := range []string{"![Animation](</tmp/result.", "gif>)"} {
		if got := projection.Speak(chunk).Speakable; got != "" {
			t.Fatalf("image path reached speech: %q", got)
		}
	}
	if got := projection.Speak("Here is your animation.").Speakable; got == "" {
		t.Fatal("image suppressed later prose")
	}
}

func TestVoiceReplyImageSurvivesPlaybackInterruption(t *testing.T) {
	_, provider, _, source, _, release := imageWorkerFixture(t, false)
	close(release)
	player := newFakePlayer(5 * time.Second)
	defer player.Close()
	capture := newFakeCapture()
	bus := events.NewBus()
	p := &Pipeline{VoiceFailedRouteRepair: true, Brain: provider, STT: &fakeSTT{text: "Show me a picture"}, Capture: capture, VAD: &fakeVAD{}, BargeInVAD: &fakeVAD{}, TTS: &fakeTTS{}, Player: player, Events: bus}
	var ready events.ResponseReady
	events.Subscribe(bus, func(e events.ResponseReady) { ready = e })
	images := make(chan events.ReplyImagesReady, 1)
	events.Subscribe(bus, func(e events.ReplyImagesReady) { images <- e })
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := p.RunTurn(ctx); done <- err }()
	var early events.ReplyImagesReady
	select {
	case early = <-images:
		if len(early.Attachments) != 1 || strings.Contains(early.Response, "![") {
			t.Fatalf("early image reply: %+v", early)
		}
	case <-ctx.Done():
		t.Fatal("no image during the spoken turn")
	}
	select {
	case <-player.StartedSignal():
	case <-ctx.Done():
		t.Fatal("playback did not start")
	}
	if !player.IsPlaying() {
		t.Fatal("the image waited for playback to finish")
	}
	if err := os.Remove(source); err != nil {
		t.Fatal(err)
	}
	time.Sleep(bargeInArmDelay + 80*time.Millisecond)
	for range bargeInMinSpeechChunks {
		capture.Publish([]float32{0.9, 0.9, 0.9})
		time.Sleep(60 * time.Millisecond)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if !ready.Interrupted || len(ready.Attachments) != 1 || ready.Attachments[0] != early.Attachments[0] || ready.Response != early.Response {
		t.Fatalf("interrupted chat reply: %+v", ready)
	}
	history := provider.History()
	last := history[len(history)-1]
	if len(last.Attachments) != 1 || last.Attachments[0] != ready.Attachments[0] {
		t.Fatalf("recorded reply: %+v", last)
	}
	if _, err := chatmedia.DefaultStore().Preview(last.Attachments[0]); err != nil {
		t.Fatal(err)
	}
}

// A worker runs unattended, so a picture job is refused before it starts when
// the worker's model cannot view images. The brain no longer refuses a
// picture on its own.
func TestTextOnlyWorkerRefusesAPictureJobBeforeItStarts(t *testing.T) {
	root := t.TempDir()
	config.SetConfigDirForTest(t, filepath.Join(root, "config"))
	ref, err := chatmedia.DefaultStore().Import("input.png", testmedia.PNG())
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tags":
			_ = json.NewEncoder(w).Encode(map[string]any{"models": []map[string]string{{"name": "fixture"}}})
		case "/api/show":
			_ = json.NewEncoder(w).Encode(map[string]any{"capabilities": []string{"completion", "tools"}})
		default:
			t.Errorf("a refused picture job reached %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	cfg := &config.Config{MaxHistory: 20, OllamaHost: server.URL, OllamaModel: "fixture", VoiceWorkerProvider: "ollama", VoiceToolsEnabled: true, WorkDir: root, PromptsDir: filepath.Join(root, "prompts"), AgentName: "Obey"}
	worker, err := brain.NewOllama(cfg)
	if err != nil {
		t.Fatal(err)
	}
	p := &Pipeline{Brain: worker, Events: events.NewBus()}
	jobs := job.NewTable(t.Context())
	defer jobs.Close()
	a := NewAgency(p, jobs, worker, cfg, nil)
	_, err = a.workerRun("Describe the picture", worker, cfg)(chatmedia.WithInput(t.Context(), []chatmedia.Ref{ref}))
	if err == nil || !strings.Contains(err.Error(), "does not support images") {
		t.Fatalf("picture job on a text-only worker: %v", err)
	}
}

func writeReplyPictures(t *testing.T, dir string, names ...string) []string {
	t.Helper()
	paths := make([]string, len(names))
	for i, name := range names {
		paths[i] = filepath.Join(dir, name)
		if err := os.WriteFile(paths[i], testmedia.SolidPNG(uint8(60*i)), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return paths
}

func TestWorkerAttachesThreePicturesWrittenOnOneLine(t *testing.T) {
	config.SetConfigDirForTest(t, t.TempDir())
	dir := t.TempDir()
	p := writeReplyPictures(t, dir, "a.png", "b.png", "c.png")
	answer := "Fresh pictures, here they are. ![A](" + p[0] + ") ![B](" + p[1] + ") ![C](" + p[2] + ")"
	cfg := &config.Config{WorkDir: dir, VoiceToolsEnabled: true}
	w := &imageRepairWorker{answers: []string{answer}}
	jobs := job.NewTable(t.Context())
	defer jobs.Close()
	a := NewAgency(&Pipeline{Events: events.NewBus()}, jobs, w, cfg, nil)
	ctx := context.WithValue(t.Context(), imageDeliveryKey{}, true)
	text, err := a.workerRunWithBudget("Make three pictures", w, cfg, brain.NewWorkerBudget(brain.BudgetLimits{}), true)(ctx)
	if err != nil || len(w.refs) != 3 || text != "Fresh pictures, here they are.\nA\nB\nC" || len(w.inputs) != 1 {
		t.Fatalf("text=%q refs=%d inputs=%d err=%v", text, len(w.refs), len(w.inputs), err)
	}
}

func streamImageReply(t *testing.T, chunks ...string) (*fakeTTS, *turnMetrics, []events.ReplyImagesReady) {
	t.Helper()
	player := newFakePlayer(time.Millisecond)
	defer player.Close()
	tts := &fakeTTS{}
	p := &Pipeline{Brain: &imageReplyBrain{fakeBrain{chunks: chunks}}, TTS: tts, Player: player, Events: events.NewBus(), VoiceFailedRouteRepair: true}
	var shown []events.ReplyImagesReady
	events.Subscribe(p.Events, func(e events.ReplyImagesReady) { shown = append(shown, e) })
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	stream, err := p.Brain.ThinkStream(ctx, "show me", brain.StreamOptions{})
	if err != nil {
		t.Fatal(err)
	}
	metrics := newTurnMetrics()
	if _, _, err := p.streamResponse(ctx, cancel, stream, false, metrics, p.newTurnConductor(metrics), &promiseRepairState{enabled: true}); err != nil {
		t.Fatal(err)
	}
	return tts, metrics, shown
}

func storePictures(t *testing.T, names ...string) []string {
	t.Helper()
	config.SetConfigDirForTest(t, filepath.Join(t.TempDir(), "config"))
	dir := chatmedia.DefaultStore().Dir
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return writeReplyPictures(t, dir, names...)
}

func TestStreamedReplyAttachesThreePicturesWrittenOnOneLine(t *testing.T) {
	p := storePictures(t, "a.png", "b.png", "c.png")
	reply := "Fresh pictures, here they are. ![A](<" + p[0] + ">) ![B](<" + p[1] + ">) ![C](<" + p[2] + ">)"
	tts, metrics, shown := streamImageReply(t, reply)
	if len(shown) != 1 || len(shown[0].Attachments) != 3 || shown[0].Response != "Fresh pictures, here they are.\nA\nB\nC" {
		t.Fatalf("shown %+v", shown)
	}
	if metrics.mediaReply == nil || len(metrics.mediaReply.Attachments) != 3 {
		t.Fatalf("terminal reply %+v", metrics.mediaReply)
	}
	if got := tts.Texts(); len(got) != 1 || got[0] != "Fresh pictures, here they are." {
		t.Fatalf("spoken %q", got)
	}
}

// The prose sharing a line with a picture is still spoken, and the turn does
// not fall back to the recovery line.
func TestOneLineImageReplySpeaksItsSentence(t *testing.T) {
	p := storePictures(t, "bird.png")
	for _, chunks := range [][]string{
		{"Here it is: ![Blue bird](<" + p[0] + ">)"},
		{"Here it is: ![Blue bird](<", p[0], ">)"},
	} {
		tts, metrics, shown := streamImageReply(t, chunks...)
		if got := tts.Texts(); len(got) != 1 || got[0] != "Here it is:" {
			t.Fatalf("%q: spoken %q", chunks, got)
		}
		if len(shown) != 1 || len(shown[0].Attachments) != 1 || shown[0].Response != "Here it is:\nBlue bird" || metrics.mediaReply == nil {
			t.Fatalf("%q: shown %+v", chunks, shown)
		}
	}
}

func TestSpeechSkipsAReferenceSplitAcrossSegments(t *testing.T) {
	projection := NewSpeechProjection()
	var spoken []string
	for _, chunk := range []string{"![A](</w/a.", "png>)", "That is all."} {
		if got := projection.Speak(chunk).Speakable; got != "" {
			spoken = append(spoken, got)
		}
	}
	if strings.Join(spoken, " ") != "That is all." {
		t.Fatalf("spoken %q", spoken)
	}
}

func TestSpeechSkipsAListOfReferences(t *testing.T) {
	projection := NewSpeechProjection()
	var spoken []string
	for _, chunk := range []string{"Here they are.\n- ![A](</w/a.png>)\n1. ![B](</w/b.", "png>)\n> ![C](/w/c.png)\n* ![D](</w/d.", "png>)\nThat is all."} {
		if got := projection.Speak(chunk).Speakable; got != "" {
			spoken = append(spoken, got)
		}
	}
	if strings.Join(spoken, " ") != "Here they are. That is all." {
		t.Fatalf("spoken %q", spoken)
	}
}

func gateSpeaks(lines ...string) []string {
	g := &voiceGate{}
	var spoken []string
	for _, line := range lines {
		if got, _ := g.filter(line); got != "" {
			spoken = append(spoken, got)
		}
	}
	return spoken
}

// Speech keeps only the prose before references that end a line. Any other
// reference drops its line, and a "![" that is not a picture is left alone.
// Only complete references that end a line are cut out of it. Any other line
// holding "![" is dropped whole, so no path or Markdown reaches speech.
func TestSpeechSpeaksOnlyTheProseBeforeTrailingReferences(t *testing.T) {
	for _, tc := range []struct {
		lines, want []string
	}{
		{[]string{"Here it is: ![a](</w/a.png>) ![b](/w/b.png)"}, []string{"Here it is:"}},
		{[]string{"Here they are, freshly made.", "![A](/w/a.jpg) ![B](/w/b.jpg)", "Next sentence here."}, []string{"Here they are, freshly made.", "Next sentence here."}},
		{[]string{"See ![a](x.png): the chart.", "Here ![a](x.png) it is.", "Next sentence here."}, []string{"Next sentence here."}},
		{[]string{"See ![a](x.png): the chart. ![b](y.png)", "Next sentence here."}, []string{"Next sentence here."}},
		{[]string{"Here it is: ![a [b] c](/w/x.png)", "Next sentence here."}, []string{"Next sentence here."}},
		{[]string{"Look ![a](b (1).png) now, okay.", "Next sentence here."}, []string{"Next sentence here."}},
		{[]string{"Write `![a](b.png)` to show a picture.", "Next sentence here."}, []string{"Next sentence here."}},
		{[]string{"a. ![x](/w/a.png)", "ii) ![y](/w/b.png)", "Next sentence here."}, []string{"Next sentence here."}},
	} {
		if got := gateSpeaks(tc.lines...); !slices.Equal(got, tc.want) {
			t.Errorf("%q: spoken %q, want %q", tc.lines, got, tc.want)
		}
	}
}

// Code that holds "![" must leave the fence state as it found it.
func TestSpeechKeepsFencesAroundCodeThatHoldsABang(t *testing.T) {
	reply := "Here's how:\n```go\nif strings.HasPrefix(s, \"![\") {\n\treturn\n}\n```\nOK?\nThat's all.\n```go\nx := y\n```\nDone."
	got, _ := (&voiceGate{}).filter(reply)
	if got != "Here's how:\nOK?\nThat's all.\nDone." {
		t.Fatalf("spoken %q", got)
	}
}

func TestSpeechKeepsRegionsAroundAReference(t *testing.T) {
	for _, tc := range []struct {
		lines, want []string
	}{
		{[]string{"Here is the markup.", "```", "![alt](a.png) renders the logo image here.", "let logo = load(path)", "```", "Okay."}, []string{"Here is the markup.", "Okay."}},
		{[]string{"I called the search tool.", "![a](b.png) shows the output of the run.", "line two raw"}, nil},
	} {
		if got := gateSpeaks(tc.lines...); !slices.Equal(got, tc.want) {
			t.Errorf("%q: spoken %q, want %q", tc.lines, got, tc.want)
		}
	}
}

func TestSpeechKeepsAnEscapedReferenceOutOfVoice(t *testing.T) {
	got := NewSpeechProjection().Speak("Write \\![demo](missing.png) like this.").Speakable
	if strings.Contains(got, "missing") || strings.Contains(got, "png") || strings.Contains(got, "demo") {
		t.Fatalf("spoken %q", got)
	}
}
