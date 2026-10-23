package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

// Desktop progress belongs to this install, not the shared speech configuration.
type setupProgress struct {
	Version   int  `json:"version"`
	Step      int  `json:"step"`
	Completed bool `json:"completed"`
	Deferred  bool `json:"deferred"`
	TextOnly  bool `json:"text_only"`
	Silent    bool `json:"silent"`
}

func setupPath() string { return filepath.Join(config.ConfigDir(), "desktop-setup.json") }

func readSetup(path string) (setupProgress, string) {
	p := setupProgress{Version: 1}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return p, ""
	}
	if err != nil {
		return p, "Could not read setup progress: " + err.Error()
	}
	if err = json.Unmarshal(data, &p); err != nil || !validSetup(p) {
		return setupProgress{Version: 1}, "Saved setup progress is invalid; start again. Your voice settings are unchanged."
	}
	return p, ""
}

func validSetup(p setupProgress) bool {
	return p.Version == 1 && p.Step >= 0 && p.Step <= 4 && !(p.Completed && p.Deferred)
}

func setupStatus() (any, error) {
	// LoadRaw does not seed personas or migrate config on a first launch.
	if _, err := config.LoadRaw(); err != nil {
		return nil, err
	}
	p, warning := readSetup(setupPath())
	_, err := os.Stat(config.ConfigFile())
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	return map[string]any{"progress": p, "warning": warning, "config_path": config.ConfigFile(), "existing": err == nil}, nil
}

func saveSetup(path string, data []byte) (setupProgress, error) {
	var p setupProgress
	if err := json.Unmarshal(data, &p); err != nil {
		return p, err
	}
	if !validSetup(p) {
		return p, errors.New("invalid desktop setup progress")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return p, err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".desktop-setup-*")
	if err != nil {
		return p, err
	}
	defer os.Remove(f.Name())
	payload, err := json.MarshalIndent(p, "", "  ")
	if err == nil {
		_, err = f.Write(payload)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(f.Name(), path)
	}
	if err != nil {
		return p, fmt.Errorf("save desktop setup: %w", err)
	}
	return p, nil
}
