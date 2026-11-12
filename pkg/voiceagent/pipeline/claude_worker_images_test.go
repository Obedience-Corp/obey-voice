package pipeline

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Obedience-Corp/obey-voice/internal/testmedia"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/chatmedia"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/job"
)

// A worker on Claude is sent the image of the line that started its job,
// whichever persona and backend answered that line: the worker provider is
// one the user configured, as with Codex.
func TestClaudeWorkerIsSentTheImageOfTheLineThatStartedIt(t *testing.T) {
	config.SetConfigDirForTest(t, t.TempDir())
	ref, err := chatmedia.DefaultStore().Import("diagram.png", testmedia.PNG())
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	stdin := filepath.Join(dir, "stdin")
	script := "#!/bin/sh\ncat > '" + stdin + "'\n" +
		`printf '%s\n' '{"type":"result","subtype":"success","result":"It is a diagram.","session_id":"w1"}'` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "claude"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	cfg := &config.Config{BrainProvider: "claude", VoiceWorkerProvider: "claude", IsWorker: true, AgentName: "Worker", MaxHistory: 10}
	worker, err := brain.NewProvider(cfg)
	if err != nil {
		t.Fatal(err)
	}
	jobs := job.NewTable(t.Context())
	t.Cleanup(jobs.Close)
	live := &Pipeline{Brain: &fakeBrain{}, Events: events.NewBus()}
	a := NewAgency(live, jobs, worker, cfg, nil)
	result, err := a.workerRun("Describe the attached image.", worker, cfg)(chatmedia.WithInput(t.Context(), []chatmedia.Ref{ref}))
	if err != nil || result != "It is a diagram." {
		t.Fatalf("worker = %q, %v", result, err)
	}
	sent, err := os.ReadFile(stdin)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(sent), base64.StdEncoding.EncodeToString(testmedia.PNG())) || !strings.Contains(string(sent), "Describe the attached image.") {
		t.Fatalf("the worker was not sent the image with its brief: %.200s", sent)
	}
}
