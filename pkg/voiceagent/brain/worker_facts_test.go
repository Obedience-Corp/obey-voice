package brain

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/ollama/ollama/api"
)

func TestWorkerFactsPromptDescribesWhatAJobIsDoing(t *testing.T) {
	leadIn := systemPrompt(nil, "active-work", nil)
	if !strings.Contains(leadIn, `started_by="you"`) {
		t.Fatalf("the lead-in does not explain the ownership mark the list uses: %q", leadIn)
	}
	got := WorkerFactsPrompt(nil, []WorkerFact{
		{ID: "job-1", Tier: "legacy", State: "running", Task: "Find the  quarterly report\nand summarise it", Running: 24*time.Minute + 13*time.Second, Progress: "read 3 files, ran 1 command", Own: true, Starter: "Mira"},
		{ID: "job-2", Tier: config.WorkerTierSimple, State: "queued", Task: `Say "hi"`, Running: time.Minute, Starter: "Ember"},
		{ID: "job-3", Tier: config.WorkerTierSimple, State: "terminal"},
	})
	want := "\n" + leadIn + "\n<active_work>" +
		"\n" + `job="job-1" tier="legacy" state="running" started_by="you" running_for="24 minutes" task="Find the quarterly report and summarise it" progress="read 3 files, ran 1 command"` +
		"\n" + `job="job-2" tier="simple" state="queued" started_by="Ember" task="Say \"hi\""` +
		"\n" + `job="job-3" tier="simple" state="terminal"` +
		"\n</active_work>"
	if got != want {
		t.Fatalf("facts prompt:\n got %q\nwant %q", got, want)
	}
	if got := WorkerFactsPrompt(nil, nil); got != "" {
		t.Fatalf("no jobs should add nothing, got %q", got)
	}
}

func TestWorkerFactsPromptKeepsEveryJobWithinItsBounds(t *testing.T) {
	long := strings.Repeat("résumé ", 200)
	facts := make([]WorkerFact, workerFactLimit)
	for i := range facts {
		facts[i] = WorkerFact{ID: "W7K2UW4JVOUNNTVWHMH3DCAPE" + string(rune('A'+i)), Tier: config.WorkerTierComplex, State: "running",
			Task: long, Running: 30 * time.Hour, Progress: long, Starter: long}
	}
	got := WorkerFactsPrompt(nil, facts)
	block := got[strings.Index(got, "\n<active_work>"):]
	if strings.Count(block, "job=") != workerFactLimit || len(block) > workerFactsBytes || !utf8.ValidString(block) {
		t.Fatalf("jobs=%d bytes=%d valid=%v", strings.Count(block, "job="), len(block), utf8.ValidString(block))
	}
	for _, line := range strings.Split(block, "\n") {
		if strings.HasPrefix(line, "job=") && strings.Count(line, "…") != 3 {
			t.Fatalf("task, progress and starter should each be cut short: %q", line)
		}
	}
}

func TestWorkerFactsPromptCountsUnfinishedJobsItCannotList(t *testing.T) {
	facts := make([]WorkerFact, 0, workerFactLimit+3)
	for i := range workerFactLimit + 1 {
		facts = append(facts, WorkerFact{ID: "job-" + string(rune('a'+i)), Tier: "legacy", State: "queued", Task: "audit the config"})
	}
	facts = append(facts, WorkerFact{ID: "job-y", Tier: "legacy", State: "running", Task: "tidy the diary"}, WorkerFact{ID: "job-z", Tier: "legacy", State: "terminal"})
	got := WorkerFactsPrompt(nil, facts)
	if strings.Count(got, "job=") != workerFactLimit || !strings.HasSuffix(got, "\nunfinished jobs not listed: 2\n</active_work>") {
		t.Fatalf("facts prompt = %q", got)
	}
}

func TestWorkerFactDurationReadsAsSpoken(t *testing.T) {
	for d, want := range map[time.Duration]string{
		400 * time.Millisecond:          "1 second",
		45 * time.Second:                "45 seconds",
		time.Minute:                     "1 minute",
		24*time.Minute + 59*time.Second: "24 minutes",
		time.Hour:                       "1 hour",
		2*time.Hour + time.Minute:       "2 hours 1 minute",
	} {
		if got := workerFactDuration(d); got != want {
			t.Errorf("%v = %q, want %q", d, got, want)
		}
	}
}

func TestActiveWorkLeadInOverrideAndMalformedFallback(t *testing.T) {
	cfg := &config.Config{PromptsDir: t.TempDir()}
	dir := filepath.Join(cfg.PromptsDir, "system")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "active-work.yaml")
	facts := []WorkerFact{{ID: "job-1", Tier: "legacy", State: "running", Task: "audit the config"}}
	valid := "schema: obey-voice.prompt.v1\nprompt:\n  name: active-work\n  kind: system\n  system_prompt: 'Edited lead-in.'\n"
	if err := os.WriteFile(path, []byte(valid), 0600); err != nil {
		t.Fatal(err)
	}
	if got := WorkerFactsPrompt(cfg, facts); !strings.HasPrefix(got, "\nEdited lead-in.\n<active_work>") {
		t.Fatal(got)
	}
	if err := os.WriteFile(path, []byte("malformed: ["), 0600); err != nil {
		t.Fatal(err)
	}
	if got := WorkerFactsPrompt(cfg, facts); !strings.HasPrefix(got, "\n"+systemPrompt(nil, "active-work", nil)+"\n<active_work>") {
		t.Fatal(got)
	}
}

// The job list describes the moment of one request. It rides beside the
// message being answered and is gone from the next request and from history,
// so a finished job cannot linger as a stale claim that it is still running.
func TestActiveWorkStaysOutOfHistoryAndTheSystemPrompt(t *testing.T) {
	fake := &jsonRouteFake{answer: "Still on it."}
	o := jsonRouteTestBrain(t, fake.server(t))
	facts := []WorkerFact{{ID: "job-1", Tier: "legacy", State: "running", Task: "audit the config", Running: 3 * time.Minute, Own: true}}
	if turn := runRoutedTurn(t, o, "What are you working on?", StreamOptions{WorkerFacts: facts}); turn.result.Err != nil {
		t.Fatal(turn.result.Err)
	}
	if turn := runRoutedTurn(t, o, "Thanks.", StreamOptions{}); turn.result.Err != nil {
		t.Fatal(turn.result.Err)
	}
	sent := fake.sent()
	if len(sent) != 2 {
		t.Fatalf("requests=%d, want 2", len(sent))
	}
	during := sent[0].Messages
	last := during[len(during)-1]
	if last.Role != "user" || !strings.HasPrefix(last.Content, "What are you working on?\n") || !strings.Contains(last.Content, `task="audit the config"`) {
		t.Fatalf("the message being answered does not carry the job: %+v", last)
	}
	if strings.Contains(during[0].Content, "active_work") {
		t.Fatalf("the job list moved into the system prompt, which would re-read the whole conversation every turn: %q", during[0].Content)
	}
	for _, m := range sent[1].Messages {
		if strings.Contains(m.Content, "active_work") || strings.Contains(m.Content, "audit the config") {
			t.Fatalf("a later request still carries the earlier job list: %+v", m)
		}
	}
	for _, turn := range o.History() {
		if strings.Contains(turn.Content, "active_work") || strings.Contains(turn.Content, "audit the config") {
			t.Fatalf("the job list was saved to history: %+v", turn)
		}
	}
}

// A persona that inspects something with a tool before it answers composes
// the answer on the second request, so the job list has to still be there.
func TestActiveWorkSurvivesTheToolRoundBeforeTheAnswer(t *testing.T) {
	o, requests := imageClaimBrain(t, []api.Message{
		{Role: "assistant", ToolCalls: []api.ToolCall{{Function: api.ToolCallFunction{Name: "list_files", Arguments: mustArgs(map[string]any{"path": "."})}}}},
		{Role: "assistant", Content: "Still auditing the config."},
	})
	o.workDir = t.TempDir()
	facts := []WorkerFact{{ID: "job-1", Tier: "legacy", State: "running", Task: "audit the config", Running: 3 * time.Minute, Own: true}}
	turn := runRoutedTurn(t, o, "What have you been doing?", StreamOptions{ToolsEnabled: true, RouterToolsPolicy: RouterToolsDirect, WorkerFacts: facts})
	if turn.result.Err != nil || len(*requests) != 2 || len(turn.tools) != 1 {
		t.Fatalf("want a tool round and an answer: %+v requests=%d tools=%+v", turn.result, len(*requests), turn.tools)
	}
	for i, req := range *requests {
		asked := ""
		for _, m := range req.Messages {
			if m.Role == "user" {
				asked = m.Content
			}
		}
		if !strings.Contains(asked, `task="audit the config"`) {
			t.Fatalf("request %d lost the job list: %q", i, asked)
		}
	}
}

func TestWorkerFactsPromptNotesSameOutputOnTheRunningJob(t *testing.T) {
	got := WorkerFactsPrompt(nil, []WorkerFact{
		{ID: "job-1", Tier: "legacy", State: "running", Task: "list the project folder", SameOutput: 12},
		{ID: "job-2", Tier: "legacy", State: "running", Task: "watch the build", SameOutput: sameOutputShown - 1},
		{ID: "job-3", Tier: "legacy", State: "queued", Task: "tidy the notes", SameOutput: 5},
	})
	lines := strings.Split(got, "\n")
	var described []string
	for _, line := range lines {
		if strings.HasPrefix(line, "job=") {
			described = append(described, line)
		}
	}
	if len(described) != 3 || !strings.HasSuffix(described[0], ` task="list the project folder" output="same output for 12 rounds"`) {
		t.Fatalf("facts prompt = %q", got)
	}
	if strings.Contains(described[1], "output=") || strings.Contains(described[2], "output=") {
		t.Fatalf("a short run or a waiting job got the note: %q", got)
	}
	leadIn := systemPrompt(nil, "active-work", nil)
	if !strings.Contains(leadIn, `output="same output for N rounds"`) || strings.Contains(strings.ToLower(leadIn), "stuck") {
		t.Fatalf("the lead-in should explain the note without calling the job stuck: %q", leadIn)
	}
}

func TestWorkerFactsPromptWithoutSameOutputIsUnchanged(t *testing.T) {
	facts := []WorkerFact{
		{ID: "job-1", Tier: "legacy", State: "running", Task: "list the project folder", Running: time.Minute, Progress: "ran 2 commands", Own: true},
		{ID: "job-2", Tier: config.WorkerTierSimple, State: "queued", Task: "tidy the notes", Starter: "Ember"},
	}
	want := WorkerFactsPrompt(nil, facts)
	for _, n := range []int{1, sameOutputShown - 1} {
		counted := slices.Clone(facts)
		counted[0].SameOutput, counted[1].SameOutput = n, n
		if got := WorkerFactsPrompt(nil, counted); got != want {
			t.Fatalf("a run of %d changed the prompt:\n got %q\nwant %q", n, got, want)
		}
	}
	if strings.Contains(want[strings.Index(want, "\n<active_work>"):], "output=") {
		t.Fatalf("no job has a count, but the prompt carries the note: %q", want)
	}
}

func TestWorkerFactsPromptKeepsItsBoundsWithSameOutput(t *testing.T) {
	long := strings.Repeat("résumé ", 200)
	facts := make([]WorkerFact, workerFactLimit+2)
	for i := range facts {
		facts[i] = WorkerFact{ID: "W7K2UW4JVOUNNTVWHMH3DCAPE" + string(rune('A'+i)), Tier: config.WorkerTierComplex, State: "running",
			Task: long, Running: 30 * time.Hour, Progress: long, Starter: long, SameOutput: 1_000_000}
	}
	got := WorkerFactsPrompt(nil, facts)
	block := got[strings.Index(got, "\n<active_work>"):]
	if strings.Count(block, "job=") != workerFactLimit || strings.Count(block, `output="same output for 1000000 rounds"`) != workerFactLimit ||
		len(block) > workerFactsBytes || !strings.Contains(block, "unfinished jobs not listed: 2") {
		t.Fatalf("jobs=%d bytes=%d block=%q", strings.Count(block, "job="), len(block), block)
	}
}
