package tui

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/pipeline"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/workfold"
)

// demoSameOutputEnabled selects a fixture job whose worker keeps rewording one
// listing command and getting the same listing back, one round a second.
func demoSameOutputEnabled() bool {
	return strings.EqualFold(strings.TrimSpace(os.Getenv(demoWorkersEnv)), "same_output")
}

var demoSameOutputCommands = []string{
	"ls", "ls -1", "ls | cat", "ls -1 | cat", "ls .", "ls ./", "ls -- .", "command ls",
	"ls | sort", "ls | cat | cat", "ls -1 .", "ls | head -n 100", "ls -1 ./", "ls -1 -- .",
}

type demoSameOutputWorkers struct {
	demoWorkerController
	start time.Time
}

var demoSameOutput = sync.OnceValue(func() workerController {
	return &demoSameOutputWorkers{start: time.Now()}
})

func (d *demoSameOutputWorkers) Workers() []pipeline.WorkerInfo {
	elapsed := time.Since(d.start)
	rounds := min(2+int(elapsed/time.Second), len(demoSameOutputCommands))
	tools := make([]workfold.Call, rounds)
	for i := range tools {
		tools[i] = workfold.Call{ID: fmt.Sprintf("call-%d", i+1), Name: "run_command", Summary: demoSameOutputCommands[i],
			Status: "completed", Preview: "README.md…", Elapsed: 20 * time.Millisecond}
	}
	return []pipeline.WorkerInfo{{
		ID: "job-1", Task: "List the files in the project folder", State: "running",
		Provider: "ollama", Model: "local-model", Tools: tools, SameOutput: rounds,
		Elapsed: 30*time.Second + elapsed.Round(time.Second),
	}}
}
