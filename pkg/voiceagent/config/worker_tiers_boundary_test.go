package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestWorkerSchemaRejectsInvalidNestedFields(t *testing.T) {
	for _, name := range []string{"missing endpoint", "null enabled", "quoted runtime", "numeric model", "null revision", "scalar assignment", "unknown limit", "empty reference"} {
		t.Run(name, func(t *testing.T) {
			SetConfigDirForTest(t, t.TempDir())
			encoded, _ := json.Marshal(validTierWorkers())
			var raw map[string]any
			if err := json.Unmarshal(encoded, &raw); err != nil {
				t.Fatal(err)
			}
			tier := raw["tiers"].(map[string]any)["simple"].(map[string]any)
			assignment := tier["assignment"].(map[string]any)
			limits := tier["limits"].(map[string]any)
			switch name {
			case "missing endpoint":
				delete(assignment, "endpoint_ref")
			case "null enabled":
				tier["enabled"] = nil
			case "quoted runtime":
				limits["runtime_seconds"] = "120"
			case "numeric model":
				assignment["model"] = 42
			case "null revision":
				raw["revision"] = nil
			case "scalar assignment":
				tier["assignment"] = "ollama"
			case "unknown limit":
				limits["requests"] = 3
			case "empty reference":
				assignment["account_ref"] = ""
			}
			data, err := yaml.Marshal(map[string]any{"voice_workers": raw})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(ConfigFile(), data, 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadWithoutHooks(); err == nil {
				t.Fatal("invalid schema accepted")
			}
		})
	}
}

// The child has independent Viper state and process-local mutexes. This
// exercises the shared file lock and the persisted revision comparison.
func TestWorkerCASProcessHelper(t *testing.T) {
	dir := os.Getenv("OBEY_TIER_CAS_TEST_DIR")
	if dir == "" {
		return
	}
	SetConfigDirForTest(t, dir)
	w := validTierWorkers()
	w.Tiers.Simple.Assignment.Model = os.Getenv("OBEY_TIER_CAS_TEST_MODEL")
	_, err := CommitVoiceWorkersCAS(w, 1)
	var writeErr *SetError
	switch {
	case err == nil:
		fmt.Print("CAS_COMMITTED")
	case errors.Is(err, ErrWorkerRevisionConflict):
		fmt.Print("CAS_CONFLICT")
	case errors.As(err, &writeErr) && writeErr.Code == CodeLocked:
		fmt.Print("CAS_LOCKED")
	default:
		t.Fatal(err)
	}
}

func TestWorkerCASAcrossProcessesAndExternalReload(t *testing.T) {
	dir := t.TempDir()
	SetConfigDirForTest(t, dir)
	if _, err := CommitVoiceWorkersCAS(validTierWorkers(), 0); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadWithoutHooks(); err != nil {
		t.Fatal(err)
	}
	commands := make([]*exec.Cmd, 2)
	outputs := make([]bytes.Buffer, 2)
	for i := range commands {
		commands[i] = exec.Command(os.Args[0], "-test.run=^TestWorkerCASProcessHelper$")
		commands[i].Env = append(os.Environ(), "OBEY_TIER_CAS_TEST_DIR="+dir, fmt.Sprintf("OBEY_TIER_CAS_TEST_MODEL=writer-%d", i))
		commands[i].Stdout, commands[i].Stderr = &outputs[i], &outputs[i]
		if err := commands[i].Start(); err != nil {
			t.Fatal(err)
		}
	}
	winners := 0
	for i, cmd := range commands {
		if err := cmd.Wait(); err != nil {
			t.Fatalf("child: %v %s", err, outputs[i].String())
		}
		if strings.Contains(outputs[i].String(), "CAS_COMMITTED") {
			winners++
		}
	}
	if winners != 1 {
		t.Fatalf("got %d committed writers", winners)
	}
	loaded, err := LoadWithoutHooks()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.VoiceWorkers.Revision != 2 || !strings.HasPrefix(loaded.VoiceWorkers.Tiers.Simple.Assignment.Model, "writer-") {
		t.Fatalf("external revision hidden by stale in-process settings: %+v", loaded.VoiceWorkers)
	}
}
