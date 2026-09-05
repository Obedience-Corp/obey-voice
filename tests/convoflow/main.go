// Command convoflow runs an opt-in scripted conversation through Obey Voice
// and keeps the recording. Not part of `just test` / CI.
//
//	just convoflow run live_model=qwen3.8:27b
//	go run ./tests/convoflow -scenario tests/convoflow/scenarios/search-web-intent.yaml
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
)

func main() {
	os.Exit(runMain(os.Args[1:]))
}

func runMain(args []string) int {
	fs := flag.NewFlagSet("convoflow", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	scenarioPath := fs.String("scenario", "", "scenario YAML (required)")
	analyzeDir := fs.String("analyze", "", "rescore an existing run directory")
	liveProvider := fs.String("live-provider", "ollama", "live brain provider")
	liveModel := fs.String("live-model", "qwen3:8b", "live model")
	workerProvider := fs.String("worker-provider", "ollama", "worker provider")
	workerModel := fs.String("worker-model", "", "worker model; empty inherits live")
	workerHost := fs.String("worker-ollama-host", "", "worker Ollama URL; empty inherits live host")
	ollamaHost := fs.String("ollama-host", "http://localhost:11434", "live Ollama URL")
	runsDir := fs.String("runs-dir", "", "artifact root (default ~/.cache/obey-voice/convoflow)")
	selfTest := fs.Bool("self-test", false, "run with a fake runner (no models)")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if *analyzeDir != "" {
		fmt.Fprintf(os.Stderr, "convoflow: -analyze is wired in the audio PR\n")
		return 1
	}
	if *scenarioPath == "" {
		fmt.Fprintf(os.Stderr, "convoflow: -scenario is required\n")
		return 1
	}

	k := knobs{
		liveProvider:     *liveProvider,
		liveModel:        *liveModel,
		workerProvider:   *workerProvider,
		workerModel:      *workerModel,
		workerOllamaHost: *workerHost,
		ollamaHost:       *ollamaHost,
	}
	if reason := skipReason(k); reason != "" && !*selfTest {
		fmt.Fprintf(os.Stderr, "convoflow: skip: %s\n", reason)
		return exitSkip
	}

	scn, err := loadScenario(*scenarioPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "convoflow: %v\n", err)
		return 1
	}
	runDir, err := defaultRunDir(*runsDir, scn.Name)
	if err != nil {
		fmt.Fprintf(os.Stderr, "convoflow: %v\n", err)
		return 1
	}
	if err := os.MkdirAll(runDir, 0o700); err != nil {
		fmt.Fprintf(os.Stderr, "convoflow: %v\n", err)
		return 1
	}
	if data, err := os.ReadFile(*scenarioPath); err == nil {
		_ = os.WriteFile(filepath.Join(runDir, "scenario.yaml"), data, 0o644)
	}
	fmt.Printf("convoflow %s → %s\n", scn.Name, runDir)

	if !*selfTest {
		fmt.Fprintf(os.Stderr, "convoflow: live agent wiring is not in this skeleton; YAML/just knobs are ready. Use -self-test for the fake runner.\n")
		return exitSkip
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	rec := &recorder{}
	eng := &engine{
		scn:    scn,
		runDir: runDir,
		rec:    rec,
		runner: &fakeRunner{live: "route_only", worker: "legacy", tools: true, liveOn: true},
	}
	if err := eng.run(ctx); err != nil {
		if errors.Is(err, context.Canceled) {
			return 1
		}
		fmt.Fprintf(os.Stderr, "convoflow: %v\n", err)
		return 1
	}
	return 0
}
