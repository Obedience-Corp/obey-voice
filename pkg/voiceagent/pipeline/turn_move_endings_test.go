package pipeline

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/internal/testmedia"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/chatmedia"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/job"
)

func (c *movingConversation) ready() events.ResponseReady {
	c.t.Helper()
	return c.next(func(e events.Event) bool { _, ok := e.(events.ResponseReady); return ok }).(events.ResponseReady)
}

func (c *movingConversation) ended() events.Event {
	c.t.Helper()
	return c.next(func(e events.Event) bool {
		switch e.(type) {
		case events.JobFinished, events.JobFailed, events.JobAborted:
			return true
		}
		return false
	})
}

// switchToObey hands the conversation to Obey's provider, built from the
// history kept for the moved reply as persona selection builds it.
func (c *movingConversation) switchToObey() *workingBrain {
	c.t.Helper()
	obey := newWorkingBrain()
	history, ok := c.p.MovedContinuity(c.p.Brain)
	if !ok {
		c.t.Fatal("no history kept for the moved reply")
	}
	if err := obey.ImportContinuity(history); err != nil {
		c.t.Fatal(err)
	}
	if err := c.p.ReplaceConversationBrain(obey, nil, &config.Config{AgentName: "Obey", ActivePersona: "obey"}, nil); err != nil {
		c.t.Fatal(err)
	}
	return obey
}

func assistantTurns(turns []brain.Turn) []brain.Turn {
	var out []brain.Turn
	for _, turn := range turns {
		if turn.Role == "assistant" {
			out = append(out, turn)
		}
	}
	return out
}

// mediaSpyBrain records reply media written to it.
type mediaSpyBrain struct {
	*workingBrain
	smu   sync.Mutex
	calls []string
}

func (b *mediaSpyBrain) SetReplyMedia(text string, refs []chatmedia.Ref) error {
	b.smu.Lock()
	b.calls = append(b.calls, text)
	b.smu.Unlock()
	return b.workingBrain.SetReplyMedia(text, refs)
}

// An image line in what the turn had read goes with the job's reply, which
// attaches it; nothing is written to the provider the reply still runs on.
func TestMovedReplyCarriesItsImageToTheJobAndLeavesItsProviderAlone(t *testing.T) {
	root := t.TempDir()
	config.SetConfigDirForTest(t, filepath.Join(root, "config"))
	if err := os.WriteFile(filepath.Join(root, "chart.gif"), testmedia.GIF(), 0o600); err != nil {
		t.Fatal(err)
	}
	c := newMovingConversation(t)
	spy := &mediaSpyBrain{workingBrain: c.anika}
	c.p.Brain = spy
	c.p.Agency.ReplaceWorker(nil, &config.Config{AgentName: "Anika", ActivePersona: "anika", WorkDir: root}, nil)
	c.anika.before, c.anika.last = []string{"Here is the chart.\n![chart](chart.gif)\nIt shows growth. Still"}, " going."
	c.holdTurn()
	c.move()
	ready := c.ready()
	obey := c.switchToObey()
	close(c.anika.release)
	finished := c.ended().(events.JobFinished)
	spy.smu.Lock()
	calls := slices.Clone(spy.calls)
	spy.smu.Unlock()
	if len(calls) != 0 {
		t.Fatalf("reply media was written to the busy provider: %q", calls)
	}
	if ready.Response != "Here is the chart." || len(ready.Attachments) != 0 {
		t.Fatalf("the turn kept %+v", ready)
	}
	if len(finished.Attachments) != 1 || strings.Contains(finished.Summary, "![") {
		t.Fatalf("the job's reply is %+v", finished)
	}
	if err := c.p.RunBackgroundTurn(t.Context()); err != nil {
		t.Fatal(err)
	}
	replies := assistantTurns(obey.History())
	if last := replies[len(replies)-1]; last.PersonaID != "anika" || len(last.Attachments) != 1 {
		t.Fatalf("saved reply = %+v", last)
	}
}

// The part kept is shown as the model wrote it, markdown included, and the
// division never falls inside a fenced code block.
func TestMovedReplyKeepsItsFormattingAndCodeBlocks(t *testing.T) {
	for _, tc := range []struct {
		name       string
		before     []string
		last, kept string
	}{
		{"markdown", []string{"Run **go build** first. Then `go test` in"}, " the package.", "Run **go build** first."},
		{"code block", []string{"Run this. Then this:\n```\nfmt.Println(1). Next\n"}, "```\nDone.", "Run this."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newMovingConversation(t)
			c.anika.before, c.anika.last = tc.before, tc.last
			c.holdTurn()
			c.move()
			ready := c.ready()
			close(c.anika.release)
			finished := c.ended().(events.JobFinished)
			if ready.Response != tc.kept || ready.Response+finished.Summary != strings.Join(tc.before, "")+tc.last {
				t.Fatalf("kept %q + delivered %q", ready.Response, finished.Summary)
			}
		})
	}
}

// Sentences whose speech never became audible are not kept as said, so the
// job's reply says them.
func TestMovedVoiceReplyDoesNotKeepSentencesNeverHeard(t *testing.T) {
	c := newMovingConversation(t)
	voice := &fakeTTS{delay: 400 * time.Millisecond}
	player := newFakePlayer(time.Millisecond)
	defer player.Close()
	c.p.ReplaceTTS(voice, nil)
	c.p.Player = player
	c.p.STT = &fakeSTT{text: "Anika, HOLD and check the build"}
	c.anika.before, c.anika.last = []string{"One is first. Two is second. "}, "Three is last."
	go func() {
		_, err := c.p.RunTurn(t.Context())
		c.turn <- err
	}()
	<-c.anika.held
	c.waitRead()
	deadline := time.Now().Add(5 * time.Second)
	for len(voice.Texts()) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("nothing was handed to speech")
		}
		time.Sleep(time.Millisecond)
	}
	c.p.StopPlaybackFrom(StopSource{Name: "keystroke", KeyRunes: 1})
	time.Sleep(800 * time.Millisecond)
	c.move()
	if ready := c.ready(); ready.Response != "" {
		t.Fatalf("the turn kept %q though none of it was heard", ready.Response)
	}
	close(c.anika.release)
	if finished := c.ended().(events.JobFinished); finished.Summary != "One is first. Two is second. Three is last." {
		t.Fatalf("the job delivered %q", finished.Summary)
	}
}

// Once a spoken reply's model has finished, asking to move it is refused at
// once instead of holding the caller.
func TestMoveIsRefusedAtOnceAfterTheModelFinished(t *testing.T) {
	c := newMovingConversation(t)
	voice := &fakeTTS{}
	player := newFakePlayer(time.Minute)
	defer player.Close()
	c.p.ReplaceTTS(voice, nil)
	c.p.Player = player
	c.p.STT = &fakeSTT{text: "Anika, check the build"}
	modelDone := make(chan struct{})
	var once sync.Once
	events.Subscribe(c.p.Events, func(events.ThinkingComplete) { once.Do(func() { close(modelDone) }) })
	go func() {
		_, err := c.p.RunTurn(t.Context())
		c.turn <- err
	}()
	select {
	case <-modelDone:
	case <-time.After(5 * time.Second):
		t.Fatal("the model never finished")
	}
	asked := time.Now()
	moved := c.p.MoveTurnToBackground("Hey Obey, what time is it?")
	took := time.Since(asked)
	c.p.StopPlayback()
	if err := <-c.turn; err != nil {
		t.Fatal(err)
	}
	if moved || took > 20*time.Millisecond {
		t.Fatalf("moved=%v after %v", moved, took)
	}
}

// straddleBrain runs a tool that starts before the move and ends after it,
// then one wholly after it.
type straddleBrain struct {
	*workingBrain
	started chan struct{}
}

func (b *straddleBrain) ThinkStream(ctx context.Context, _ string, opts brain.StreamOptions) (*brain.Stream, error) {
	out, done := make(chan string), make(chan brain.StreamResult, 1)
	go func() {
		defer close(done)
		defer close(out)
		select {
		case out <- "Working on it. ":
		case <-ctx.Done():
			done <- brain.StreamResult{Err: ctx.Err()}
			return
		}
		opts.OnToolStart(brain.ToolCall{ID: "call-1", Name: "run_shell"})
		close(b.started)
		select {
		case <-b.release:
		case <-ctx.Done():
			done <- brain.StreamResult{Err: ctx.Err()}
			return
		}
		opts.OnToolEnd(brain.ToolResult{ToolCall: brain.ToolCall{ID: "call-1", Name: "run_shell"}})
		opts.OnToolStart(brain.ToolCall{ID: "call-2", Name: "read_file"})
		opts.OnToolEnd(brain.ToolResult{ToolCall: brain.ToolCall{ID: "call-2", Name: "read_file"}})
		select {
		case out <- "Done.":
		case <-ctx.Done():
		}
		done <- brain.StreamResult{}
	}()
	return &brain.Stream{Chunks: out, Done: done}, nil
}

// A tool running at the move is put on the job's row, where its outcome then
// shows, like a tool started after the move.
func TestToolRunningAtTheMoveShowsItsOutcomeOnTheJobRow(t *testing.T) {
	c := newMovingConversation(t)
	sb := &straddleBrain{workingBrain: c.anika, started: make(chan struct{})}
	c.p.Brain = sb
	var mu sync.Mutex
	var seen []string
	events.Subscribe(c.p.Events, func(e events.ToolCallStarted) {
		mu.Lock()
		seen = append(seen, "start "+e.JobID+" "+e.ID)
		mu.Unlock()
	})
	events.Subscribe(c.p.Events, func(e events.ToolCallFinished) {
		mu.Lock()
		seen = append(seen, "finish "+e.JobID+" "+e.ID)
		mu.Unlock()
	})
	go func() { c.turn <- c.p.RunTurnTextMode(t.Context(), "Anika, run it") }()
	<-sb.started
	c.waitRead()
	c.move()
	id := c.movedJob().ID
	close(c.anika.release)
	c.ended()
	mu.Lock()
	defer mu.Unlock()
	want := []string{"start  call-1", "start " + id + " call-1", "finish " + id + " call-1", "start " + id + " call-2", "finish " + id + " call-2"}
	if !slices.Equal(seen, want) {
		t.Fatalf("tool events %q, want %q", seen, want)
	}
	if calls := c.p.Agency.work.Calls(id); len(calls) != 2 {
		t.Fatalf("the job's row lists %+v", calls)
	}
}

// A moved reply that is stopped, by the user or by the conversation
// closing, is recorded as a stopped live reply is: the part kept marked
// unfinished, and the text after it, under the persona.
func TestStoppedMovedReplyIsRecordedUnfinished(t *testing.T) {
	for _, how := range []string{"stopped by the user", "conversation closed"} {
		t.Run(how, func(t *testing.T) {
			c := newMovingConversation(t)
			c.anika.before = []string{"One is done. Two is"}
			c.holdTurn()
			c.move()
			id := c.movedJob().ID
			obey := c.switchToObey()
			switch how {
			case "stopped by the user":
				c.jobs.Abort(id)
				c.ended()
				c.p.moves.recording.Wait()
				if err := c.p.RunBackgroundTurn(t.Context()); err != nil {
					t.Fatal(err)
				}
			default:
				if err := c.p.CloseWork(); err != nil {
					t.Fatal(err)
				}
			}
			replies := assistantTurns(obey.History())
			if len(replies) != 2 {
				t.Fatalf("Obey's history holds %+v", replies)
			}
			for i, want := range []string{"One is done.", "Two is"} {
				if replies[i].Content != want || replies[i].PersonaID != "anika" || replies[i].Disposition != "interrupted" {
					t.Fatalf("reply %d = %+v", i, replies[i])
				}
			}
		})
	}
}

// When the next turn stays on the moved reply's provider, the provider
// records the stopped request itself, once, and the job is not shown as one
// the user stopped.
func TestReclaimedMovedReplyIsRecordedOnceAndSaysWhy(t *testing.T) {
	c := newMovingConversation(t)
	c.holdTurn()
	c.move()
	if err := c.p.RunTurnTextMode(t.Context(), "Hey Obey, what time is it?"); err != nil {
		t.Fatal(err)
	}
	aborted := c.ended().(events.JobAborted)
	if aborted.Reason != events.AbortNotTakenOver {
		t.Fatalf("job ended as %+v", aborted)
	}
	c.p.moves.recording.Wait()
	if err := c.p.RunBackgroundTurn(t.Context()); err != nil {
		t.Fatal(err)
	}
	held := 0
	for _, turn := range assistantTurns(c.anika.History()) {
		if strings.Contains(turn.Content, "Working on it.") {
			held++
		}
	}
	if held != 1 {
		t.Fatalf("the reclaimed reply is recorded %d times: %+v", held, c.anika.History())
	}
}

// A hand-off over its budget is stopped as over budget even when its worker
// ignores being stopped, and /workers shows the worker that ran it.
func TestMovedHandOffIsHeldToItsBudgetAndShowsItsWorker(t *testing.T) {
	c := newMovingConversation(t, job.WithBudget(150*time.Millisecond))
	worker := &stubbornWorker{started: make(chan struct{}), release: make(chan struct{})}
	t.Cleanup(func() { close(worker.release) })
	c.p.Agency.ReplaceWorker(worker, &config.Config{AgentName: "Anika", ActivePersona: "anika", VoiceWorkerProvider: "ollama", VoiceWorkerModel: "worker-model"}, nil)
	c.anika.route = "spawn"
	c.holdTurn()
	c.move()
	id := c.movedJob().ID
	close(c.anika.release)
	<-worker.started
	info := c.movedJob()
	if info.ID != id || info.Provider != "ollama" || info.Model != "worker-model" {
		t.Fatalf("/workers shows %+v", info)
	}
	if aborted, ok := c.ended().(events.JobAborted); !ok || aborted.Reason != events.AbortBudget {
		t.Fatalf("the hand-off over its budget ended as %+v", aborted)
	}
}

// stubbornWorker starts and then ignores being stopped until released.
type stubbornWorker struct {
	recordingWorker
	started, release chan struct{}
}

func (w *stubbornWorker) ThinkFull(context.Context, string, brain.StreamOptions) (string, error) {
	close(w.started)
	<-w.release
	return "late", nil
}

// Retiring a provider waits for a moved request only while it runs; one told
// to stop that does not end is waited for no longer than a stopped request.
func TestClosingDoesNotWaitLongForAMovedRequestThatIgnoresStopping(t *testing.T) {
	SetProviderStopTimeoutForTest(t, 100*time.Millisecond)
	c := newMovingConversation(t)
	c.anika.stubborn = true
	t.Cleanup(func() { close(c.anika.release) })
	c.holdTurn()
	c.move()
	if err := c.p.ReplaceConversationBrain(newWorkingBrain(), nil, &config.Config{}, nil); err != nil {
		t.Fatal(err)
	}
	closed := make(chan error, 1)
	go func() { closed <- c.p.CloseBrains() }()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(providerSettleTimeout + 3*time.Second):
		t.Fatal("closing waited for a stopped request that never ended")
	}
}

// Text the turn had shown is kept or delivered even when the move lands
// while that text is still being recorded.
func TestTextShownBeforeTheMoveIsNeverLost(t *testing.T) {
	c := newMovingConversation(t)
	c.anika.trickle, c.anika.gap = 1, time.Millisecond
	c.holdTurn()
	c.p.activeMu.Lock()
	rec := c.p.activeMetrics.recording
	c.p.activeMu.Unlock()
	// The forwarder reads afterSend until it has recorded the chunk the turn
	// just read.
	for start := time.Now(); rec.deliveredText() != "Working on it. "; {
		if time.Since(start) > 5*time.Second {
			t.Fatal("the first chunk was never recorded")
		}
		time.Sleep(time.Millisecond)
	}
	unblock, inHand := make(chan struct{}), make(chan struct{})
	rec.afterSend = func(text string) {
		if strings.HasPrefix(text, "Still working") {
			close(inHand)
			<-unblock
		}
	}
	close(c.anika.release)
	<-inHand
	deadline := time.Now().Add(5 * time.Second)
	for {
		c.readMu.Lock()
		read := c.read.String()
		c.readMu.Unlock()
		if strings.Contains(read, "Still working") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the turn never showed the text")
		}
		time.Sleep(time.Millisecond)
	}
	// The recording resumes while the turn takes the move.
	names := c.p.NamesOtherPersona
	var resume sync.Once
	c.p.NamesOtherPersona = func(input string) bool {
		ok := names(input)
		if ok {
			resume.Do(func() { close(unblock) })
		}
		return ok
	}
	c.move()
	ready := c.ready()
	finished := c.ended().(events.JobFinished)
	if got := ready.Response + finished.Summary; got != "Working on it. Still working. Found three files." {
		t.Fatalf("kept + delivered = %q", got)
	}
}

// A moved reply whose model adds nothing after the part kept finishes
// without saying anything.
func TestMovedReplyThatWasAllKeptFinishesQuietly(t *testing.T) {
	c := newMovingConversation(t)
	c.anika.before, c.anika.none = []string{"It is all here. "}, true
	ended, _, voice := endMovedReply(t, c)
	finished, ok := ended.(events.JobFinished)
	if !ok || strings.TrimSpace(finished.Summary) != "" || finished.SpeechPending {
		t.Fatalf("job ended as %+v", ended)
	}
	time.Sleep(100 * time.Millisecond)
	if texts := voice.Texts(); len(texts) != 0 {
		t.Fatalf("the finished job said %q", texts)
	}
}

// A moved reply that reached its output limit is shown and saved as cut
// short but not spoken, as a live reply stops speaking there.
func TestMovedReplyAtItsOutputLimitIsShownButNotSpoken(t *testing.T) {
	c := newMovingConversation(t)
	c.anika.fail, c.anika.recovered, c.anika.failText = brain.ErrResponseLimit, true, "Found three"
	ended, obey, voice := endMovedReply(t, c)
	finished, ok := ended.(events.JobFinished)
	if !ok || !finished.CutShort || finished.SpeechPending || finished.Summary != " Found three" {
		t.Fatalf("job ended as %+v", ended)
	}
	if err := c.p.RunBackgroundTurn(t.Context()); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	if slices.Contains(voice.Texts(), "Found three") {
		t.Fatalf("the reply past its limit was spoken: %q", voice.Texts())
	}
	replies := assistantTurns(obey.History())
	if last := replies[len(replies)-1]; last.Content != "Found three" || last.Disposition != brain.IncompleteDisposition(brain.ErrResponseLimit) {
		t.Fatalf("saved reply = %+v", last)
	}
}

// Work refused under the experimental work policy is reported in the same
// words as before.
func TestExperimentalWorkRefusalKeepsItsWording(t *testing.T) {
	if got := errExperimentalPaidWork.Error(); got != "Experimental work requires a local worker; paid and unknown providers are disabled." {
		t.Fatalf("refusal reads %q", got)
	}
	if !errors.Is(error(errExperimentalPaidWork), errExperimentalPaidWork) {
		t.Fatal("the refusal does not match itself")
	}
}
