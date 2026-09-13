// Command convoflow runs an opt-in scripted conversation through Obey Voice
// and keeps the recording. Not part of `just test` / CI.
//
//	just convoflow run live_model=qwen3.8:27b
//	go run ./tests/convoflow -scenario tests/convoflow/scenarios/search-web-intent.yaml
package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio/playback"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"

	"github.com/Obedience-Corp/obey-voice/internal/persona"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

func main() {
	os.Exit(runMain(os.Args[1:]))
}

func runMain(args []string) int {
	fs := flag.NewFlagSet("convoflow", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	scenarioPath := fs.String("scenario", "", "scenario YAML (required)")
	analyzeDir := fs.String("analyze", "", "rescore an existing run directory")
	compare := fs.String("compare", "", "compare two latency report JSON files separated by comma")
	prepareSuite := fs.String("prepare-acoustic-suite", "", "generate repeatable acoustic scenarios into OUTPUTDIR")
	fixtureDir := fs.String("fixture-dir", "", "recorded acoustic fixture cache containing synth-01..03 and counting WAV/JSON pairs")
	liveProvider := fs.String("live-provider", "", "live brain provider (default ollama)")
	liveModel := fs.String("live-model", "", "live model (default qwen3:8b)")
	workerProvider := fs.String("worker-provider", "", "worker provider (default ollama; grok, claude, or stub for self-test)")
	workerModel := fs.String("worker-model", "", "worker model; empty inherits live")
	workerHost := fs.String("worker-ollama-host", "", "worker Ollama URL; empty inherits live host")
	ollamaHost := fs.String("ollama-host", "", "live Ollama URL (default http://localhost:11434)")
	runsDir := fs.String("runs-dir", "", "artifact root (default ~/.cache/obey-voice/convoflow)")
	fixtureConfig := fs.String("fixture-config", "", "explicit isolated config file (required for persona and latency runs)")
	personaDir := fs.String("persona-dir", "", "explicit persona catalog directory for persona proof")
	selfTest := fs.Bool("self-test", false, "run with a fake runner (no models)")
	gate := fs.Bool("gate", false, "gate mode: refuse the stub worker (set by `just convoflow gate`)")
	speakers := fs.Bool("speakers", false, "tee recorded PCM to real speakers")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if *compare != "" {
		parts := strings.Split(*compare, ",")
		if len(parts) != 2 {
			fmt.Fprintln(os.Stderr, "convoflow: -compare requires two comma-separated metrics files")
			return 2
		}
		report, err := compareWorkLatencyFiles(strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1]))
		data, marshalErr := json.MarshalIndent(report, "", "  ")
		if marshalErr != nil {
			fmt.Fprintln(os.Stderr, marshalErr)
			return 1
		}
		fmt.Println(string(data))
		if err != nil {
			fmt.Fprintf(os.Stderr, "convoflow: %v\n", err)
			return 1
		}
		return 0
	}
	if *prepareSuite != "" {
		if err := prepareAcousticSuite(*prepareSuite, *fixtureDir); err != nil {
			fmt.Fprintf(os.Stderr, "convoflow: %v\n", err)
			return 1
		}
		return 0
	}
	if *analyzeDir != "" {
		if err := analyzeRunDir(*analyzeDir); err != nil {
			fmt.Fprintf(os.Stderr, "convoflow: %v\n", err)
			return 1
		}
		return 0
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
		gate:             *gate,
	}

	scn, err := loadScenario(*scenarioPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "convoflow: %v\n", err)
		return 1
	}
	if *selfTest && scn.Name != "self-test" && scn.Name != "persona-proof" {
		fmt.Fprintf(os.Stderr, "convoflow: -self-test only runs scenarios/self-test.yaml or persona-proof.yaml (got %q); stock gates need a live agent\n", scn.Name)
		return 1
	}
	if *fixtureConfig != "" {
		if _, err := os.Stat(*fixtureConfig); err != nil {
			fmt.Fprintf(os.Stderr, "convoflow: fixture config %q: %v\n", *fixtureConfig, err)
			return 1
		}
	}
	var probeBase *config.Config
	if *fixtureConfig != "" {
		probeBase, err = loadFixtureConfig(*fixtureConfig)
		if err != nil {
			fmt.Fprintf(os.Stderr, "convoflow: config: %v\n", err)
			return 1
		}
	}
	if *personaDir != "" {
		if st, err := os.Stat(*personaDir); err != nil || !st.IsDir() {
			if err == nil {
				err = fmt.Errorf("not a directory")
			}
			fmt.Fprintf(os.Stderr, "convoflow: persona catalog %q: %v\n", *personaDir, err)
			return 1
		}
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	// Probes run before anything is constructed: a missing daemon or model is
	// a skip, and there is nothing to tear down yet. They see the effective
	// knobs — CLI over the scenario's brain / worker blocks — which is what
	// resolveConfig builds the run from.
	k = k.withFixtureConfig(probeBase, scn).withDefaults()
	if reason := skipReason(k); reason != "" && !*selfTest {
		fmt.Fprintf(os.Stderr, "convoflow: skip: %s\n", reason)
		return exitSkip
	}
	if !*selfTest {
		if reason := probeOllama(ctx, k); reason != "" {
			fmt.Fprintf(os.Stderr, "convoflow: skip: %s\n", reason)
			return exitSkip
		}
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

	rec := &recorder{}
	player := newRecordingPlayer(nil)
	if *speakers || scn.Capture.Speakers {
		real := playback.NewPlayerWithDevice("")
		defer func() { _ = real.Close() }()
		player = newRecordingPlayer(real)
	}
	eng := &engine{scn: scn, runDir: runDir, rec: rec, player: player, fixtureConfig: *fixtureConfig, personaDir: *personaDir}

	if *selfTest {
		if scn.Name == "persona-proof" {
			code := wirePersonaSelfTest(ctx, eng)
			if code != 0 {
				return code
			}
		} else {
			eng.runner = &fakeRunner{rec: rec, live: "route_only", liveOn: true}
			eng.profile = runProfile{Profile: "self-test", LiveProvider: "fake", WorkerProvider: "fake", Input: scn.Input}
		}
	} else {
		code := wireLiveAgent(ctx, eng, k)
		if code != 0 {
			return code
		}
	}
	if err := eng.run(ctx); err != nil {
		if errors.Is(err, context.Canceled) {
			return 1
		}
		fmt.Fprintf(os.Stderr, "convoflow: %v\n", err)
		return 1
	}
	fmt.Printf("convoflow %s: pass — %s\n", scn.Name, filepath.Join(runDir, "report.md"))
	return 0
}

// wireLiveAgent builds the production agent onto eng. Returns a process exit
// code: 0 wired, 2 skipped (missing TTS assets or tools forced off), 1 failed.
func wireLiveAgent(ctx context.Context, eng *engine, k knobs) int {
	var base *config.Config
	var err error
	if eng.fixtureConfig != "" {
		base, err = loadFixtureConfig(eng.fixtureConfig)
	} else {
		base, err = config.Load()
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "convoflow: config: %v\n", err)
		return 1
	}
	cfg, prof, err := resolveConfig(base, eng.scn, k, eng.runDir)
	if err != nil {
		return reportSetupError(err)
	}
	// Playback must honor the same resolved output as the acoustic stimulus.
	// No device has been opened yet; the runner takes ownership below.
	if eng.player != nil && eng.player.real != nil && cfg.OutputDevice != "" {
		if err := eng.player.real.Close(); err != nil {
			return reportSetupError(err)
		}
		eng.player.real = playback.NewPlayerWithDevice(cfg.OutputDevice)
	}
	resolvedBase := cfg
	var catalog persona.Catalog
	var initial *persona.SessionBinding
	if eng.scn.Persona.Initial != "" {
		if eng.fixtureConfig == "" || eng.personaDir == "" {
			return reportSetupError(fmt.Errorf("persona scenario %q requires -fixture-config and -persona-dir", eng.scn.Name))
		}
		catalog = persona.Catalog{Dir: eng.personaDir}
		initial, err = catalog.ResolveBinding(resolvedBase, eng.scn.Persona.Initial)
		if err != nil {
			return reportSetupError(err)
		}
		cfg = initial.Config()
		prof.FixtureConfig = eng.fixtureConfig
		prof.PersonaDir = eng.personaDir
	}
	if scn := eng.scn; scn.LatencyCondition != "" {
		if eng.fixtureConfig == "" {
			return reportSetupError(fmt.Errorf("latency run requires explicit isolated -fixture-config"))
		}
		pinned := *cfg
		pinned.WorkDir = ""
		data, err := json.Marshal(pinned)
		if err != nil {
			return reportSetupError(err)
		}
		host, _ := os.Hostname()
		executable, err := os.Executable()
		if err != nil {
			return reportSetupError(err)
		}
		binary, err := os.ReadFile(executable)
		if err != nil {
			return reportSetupError(err)
		}
		eng.latencyProvenance = fmt.Sprintf("%x/%x/%s/%s/%s", sha256.Sum256(data), sha256.Sum256(binary), runtime.GOOS, runtime.GOARCH, host)
	}
	if err := writeResolvedConfig(eng.runDir, prof); err != nil {
		fmt.Fprintf(os.Stderr, "convoflow: %v\n", err)
		return 1
	}

	bus := events.NewBus()
	bus.SubscribeAll(eng.rec.add)
	evlog, err := openEventLog(filepath.Join(eng.runDir, "events.jsonl"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "convoflow: %v\n", err)
		return 1
	}
	bus.SubscribeAll(evlog.add)
	eng.onClose = append(eng.onClose, func() { _ = evlog.Close() })

	if prof.silent() {
		eng.player = nil
	}
	logf := func(format string, args ...any) {
		fmt.Fprintf(os.Stderr, "convoflow: "+format+"\n", args...)
	}
	runner, err := newLiveRunner(ctx, cfg, k, bus, eng.rec, eng.player, prof.silent(), logf)
	if err != nil {
		return reportSetupError(err)
	}
	eng.runner = runner
	eng.profile = prof
	if initial == nil && cfg.VoiceWorkEnabled {
		if err := runner.agent.EnableConversationWork(filepath.Base(eng.runDir), nil, cfg); err != nil {
			runner.close()
			return reportSetupError(err)
		}
	}
	if initial != nil {
		if err := configurePersona(runner, resolvedBase, catalog, initial, eng.runDir); err != nil {
			runner.close()
			return reportSetupError(err)
		}
	}
	fmt.Printf("convoflow live=%s worker=%s tts=%s\n",
		describeBrain(prof.LiveProvider, prof.LiveModel, ""),
		describeBrain(prof.WorkerProvider, prof.WorkerModel, ""),
		prof.TTSProvider)
	return 0
}

func reportSetupError(err error) int {
	var skip skipError
	if errors.As(err, &skip) {
		fmt.Fprintf(os.Stderr, "convoflow: skip: %s\n", skip.reason)
		return exitSkip
	}
	fmt.Fprintf(os.Stderr, "convoflow: %v\n", err)
	return 1
}
