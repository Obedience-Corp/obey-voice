package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"gopkg.in/yaml.v3"
)

type acousticFixture struct {
	Path   string `json:"path"`
	Text   string `json:"text"`
	SHA256 string `json:"sha256"`
}

// prepareAcousticSuite materializes repeatable scenario files from an
// operator-supplied recorded-fixture cache. It performs no config loading,
// model lookup, generation, network access, or device I/O.
func prepareAcousticSuite(outputDir, fixtureDir string) error {
	if outputDir == "" || fixtureDir == "" {
		return errors.New("acoustic suite: output and fixture directories are required")
	}
	var err error
	outputDir, err = filepath.Abs(outputDir)
	if err != nil {
		return fmt.Errorf("output directory: %w", err)
	}
	fixtureDir, err = filepath.Abs(fixtureDir)
	if err != nil {
		return fmt.Errorf("fixture directory: %w", err)
	}
	entries, err := os.ReadDir(fixtureDir)
	if err != nil {
		return fmt.Errorf("fixture directory: %w", err)
	}
	var fixtures []acousticFixture
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(strings.ToLower(entry.Name()), ".wav") {
			continue
		}
		base := strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name()))
		if !strings.HasPrefix(strings.ToLower(base), "synth-") {
			continue
		}
		path := filepath.Join(fixtureDir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if _, _, err := audio.ReadWAVFloat32(path); err != nil {
			return fmt.Errorf("fixture %s is not a valid WAV: %w", entry.Name(), err)
		}
		meta, err := readAcousticMetadata(filepath.Join(fixtureDir, base+".json"))
		if err != nil {
			return err
		}
		h := sha256.Sum256(data)
		fixtures = append(fixtures, acousticFixture{Path: path, Text: meta, SHA256: hex.EncodeToString(h[:])})
	}
	if len(fixtures) != 3 {
		return fmt.Errorf("acoustic suite: need exactly synth-01.wav, synth-02.wav and synth-03.wav, found %d", len(fixtures))
	}
	sort.Slice(fixtures, func(i, j int) bool { return fixtures[i].Path < fixtures[j].Path })
	for i, f := range fixtures {
		if filepath.Base(f.Path) != fmt.Sprintf("synth-%02d.wav", i+1) {
			return errors.New("acoustic suite: fixture names must be synth-01.wav through synth-03.wav")
		}
	}
	counting := filepath.Join(fixtureDir, "counting.wav")
	countingText, err := readAcousticMetadata(filepath.Join(fixtureDir, "counting.json"))
	if err != nil {
		return fmt.Errorf("counting fixture: %w", err)
	}
	if _, _, err := audio.ReadWAVFloat32(counting); err != nil {
		return fmt.Errorf("counting fixture: %w", err)
	}
	countingBytes, err := os.ReadFile(counting)
	if err != nil {
		return fmt.Errorf("counting fixture: %w", err)
	}
	countingHash := sha256.Sum256(countingBytes)
	manifest := struct {
		Fixtures []acousticFixture `json:"fixtures"`
	}{append(fixtures, acousticFixture{Path: counting, Text: countingText, SHA256: hex.EncodeToString(countingHash[:])})}
	var short *acousticFixture
	shortPath := filepath.Join(fixtureDir, "startup-short.wav")
	if _, err := os.Stat(shortPath); err == nil {
		text, err := readAcousticMetadata(filepath.Join(fixtureDir, "startup-short.json"))
		if err != nil {
			return fmt.Errorf("startup-short fixture: %w", err)
		}
		if text != "Wait." {
			return fmt.Errorf("startup-short fixture: expected metadata text %q, got %q", "Wait.", text)
		}
		if _, _, err := audio.ReadWAVFloat32(shortPath); err != nil {
			return fmt.Errorf("startup-short fixture: %w", err)
		}
		data, err := os.ReadFile(shortPath)
		if err != nil {
			return fmt.Errorf("startup-short fixture: %w", err)
		}
		hash := sha256.Sum256(data)
		short = &acousticFixture{Path: shortPath, Text: text, SHA256: hex.EncodeToString(hash[:])}
		manifest.Fixtures = append(manifest.Fixtures, *short)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("startup-short fixture: %w", err)
	}
	var generated []struct {
		name string
		data []byte
	}
	write := func(name, condition string, turns []map[string]any) error {
		doc := map[string]any{"name": name, "description": "Generated repeatable acoustic latency suite", "input": "speech", "latency_condition": condition, "duration": "30m", "capture": map[string]any{"microphone": true, "audio": true, "speakers": true}, "turns": turns}
		if condition == "worker" {
			doc["worker"] = map[string]any{"sustained_load": true}
		}
		data, err := yaml.Marshal(doc)
		if err != nil {
			return err
		}
		if _, err := loadScenarioString(name, string(data)); err != nil {
			return err
		}
		generated = append(generated, struct {
			name string
			data []byte
		}{name, data})
		return nil
	}
	turns := func() []map[string]any {
		out := make([]map[string]any, 0, 33)
		for i := 0; i < 33; i++ {
			f := fixtures[i%3]
			out = append(out, map[string]any{"id": fmt.Sprintf("turn-%02d", i+1), "latency_iteration": i + 1, "latency_warmup": i < 3, "timeout": "90s", "wav": f.Path, "expected_transcript": f.Text})
		}
		return out
	}

	if err := write("baseline-33", "baseline", turns()); err != nil {
		return err
	}
	if err := write("smoke", "baseline", []map[string]any{{"id": "smoke", "latency_iteration": 1, "latency_warmup": true, "timeout": "120s", "wav": fixtures[0].Path, "expected_transcript": fixtures[0].Text}}); err != nil {
		return err
	}
	workerTurns := make([]map[string]any, 0, 99)
	for _, measured := range turns() {
		id := measured["id"].(string)
		workerTurns = append(workerTurns, map[string]any{"id": id + "-start", "op": "start_latency_worker", "timeout": "30s", "user": "report one short local worker result"})
		workerTurns = append(workerTurns, measured)
		workerTurns = append(workerTurns, map[string]any{"id": id + "-stop", "op": "stop_latency_worker", "timeout": "30s"})
	}
	if err := write("worker-33", "worker", workerTurns); err != nil {
		return err
	}
	if err := write("completion", "", []map[string]any{{"id": "completion", "op": "acoustic_completion", "timeout": "120s", "wav": fixtures[1].Path, "expected_transcript": fixtures[1].Text}}); err != nil {
		return err
	}
	if err := write("response", "", []map[string]any{{"id": "response", "op": "acoustic_response", "timeout": "180s", "duration": "3s", "wav": counting, "expected_transcript": countingText}}); err != nil {
		return err
	}
	if err := write("interruption", "", []map[string]any{{"id": "interruption", "op": "acoustic_interrupt", "timeout": "120s", "wav": counting, "interrupt_wav": fixtures[1].Path, "expected_transcript": fixtures[1].Text}}); err != nil {
		return err
	}
	if err := write("startup-full", "", []map[string]any{{"id": "startup-full", "op": "acoustic_interrupt", "timeout": "120s", "wav": counting, "interrupt_wav": fixtures[1].Path, "expected_transcript": fixtures[1].Text, "stimulus_delay": "0s"}}); err != nil {
		return err
	}
	if short != nil {
		if err := write("startup-short", "", []map[string]any{{"id": "startup-short", "op": "acoustic_interrupt", "timeout": "120s", "wav": counting, "interrupt_wav": short.Path, "expected_transcript": short.Text, "stimulus_delay": "0s", "stimulus_complete_within": "600ms"}}); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(outputDir, 0o700); err != nil {
		return err
	}
	if short == nil {
		if err := os.Remove(filepath.Join(outputDir, "startup-short.yaml")); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove stale startup-short scenario: %w", err)
		}
	}
	b, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(outputDir, "manifest.json"), append(b, '\n'), 0600); err != nil {
		return err
	}
	for _, file := range generated {
		if err := os.WriteFile(filepath.Join(outputDir, file.name+".yaml"), file.data, 0600); err != nil {
			return err
		}
	}
	return nil

}

func readAcousticMetadata(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("metadata %s: %w", filepath.Base(path), err)
	}
	var v struct {
		Text       string `json:"text"`
		Transcript string `json:"transcript"`
		Expected   string `json:"expected_transcript"`
	}
	if err := json.Unmarshal(b, &v); err != nil {
		return "", fmt.Errorf("metadata %s: %w", filepath.Base(path), err)
	}
	for _, text := range []string{v.Text, v.Transcript, v.Expected} {
		if strings.TrimSpace(text) != "" {
			return strings.TrimSpace(text), nil
		}
	}
	return "", fmt.Errorf("metadata %s: missing text/transcript", filepath.Base(path))
}
