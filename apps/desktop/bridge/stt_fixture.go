//go:build desktopfixture

package main

// The native-window harness uses real configuration writes and deterministic
// downloads. It cannot run unless fixture.go has isolated the config root.
import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

func runSTTModelFixture(dir string) {
	if os.Getenv("OBEY_VOICE_STT_SETUP_FIXTURE") != "1" || len(os.Args) < 3 || os.Args[1] != "models" {
		return
	}
	operation := os.Args[2]
	if operation != "status" && operation != "ensure" {
		return
	}
	cfg, err := config.LoadRaw()
	if err != nil {
		panic(err)
	}
	spec, model, err := config.STTModelSetting(cfg)
	if err != nil {
		panic(err)
	}
	log, err := os.OpenFile(filepath.Join(dir, "stt-commands.jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		panic(err)
	}
	_ = json.NewEncoder(log).Encode(map[string]any{"args": os.Args[1:], "key": spec.Key, "model": model})
	_ = log.Close()
	emit := func(v any) { _ = json.NewEncoder(os.Stdout).Encode(v) }
	marker := filepath.Join(dir, "stt-fixture-installed")
	if operation == "status" {
		installed, _ := os.ReadFile(marker)
		emit([]map[string]any{{"name": model, "installed": string(installed) == model}})
		os.Exit(0)
	}
	attemptPath := filepath.Join(dir, "stt-fixture-attempt")
	data, _ := os.ReadFile(attemptPath)
	attempt, _ := strconv.Atoi(string(data))
	attempt++
	_ = os.WriteFile(attemptPath, []byte(strconv.Itoa(attempt)), 0600)
	emit(map[string]any{"asset": model, "phase": "download", "pct": 25})
	if attempt == 1 {
		emit(map[string]any{"done": true, "error": "Fixture download interrupted; use Install / repair to retry."})
		os.Exit(1)
	}
	if attempt == 2 {
		time.Sleep(2 * time.Minute)
	} // canceled through the native UI
	_ = os.WriteFile(marker, []byte(model), 0600)
	emit(map[string]any{"asset": model, "phase": "done", "pct": 100})
	emit(map[string]any{"done": true, "installed": []string{model}})
	os.Exit(0)
}
