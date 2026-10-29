package config

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/spf13/viper"
)

// LoadSpeechFile reads an explicit finite-job configuration. It never reads
// global config, environment overrides, personas or correction settings.
func LoadSpeechFile(path string) (*Config, error) {
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("speech-config must be an absolute path")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open speech config: %w", err)
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, (1<<20)+1))
	if err != nil {
		return nil, err
	}
	if len(data) > 1<<20 {
		return nil, fmt.Errorf("speech config exceeds 1 MiB")
	}
	var doc struct {
		Schema    string `json:"schema"`
		Provider  string `json:"provider"`
		Model     string `json:"model"`
		ModelsDir string `json:"models_dir"`
		Language  string `json:"language"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parse speech config: %w", err)
	}
	if doc.Schema != "obey-voice.speech-config/v1" || doc.Provider != STTProviderParakeet || !filepath.IsAbs(doc.ModelsDir) || doc.Language != "en" {
		return nil, fmt.Errorf("speech config requires schema obey-voice.speech-config/v1, provider parakeet, language en and absolute models_dir")
	}
	if doc.Model != "tdt-0.6b-v2" {
		return nil, fmt.Errorf("speech config model %q is not supported", doc.Model)
	}
	vp := viper.New()
	setDefaults(vp)
	var cfg Config
	if err := vp.Unmarshal(&cfg); err != nil {
		return nil, err
	}
	cfg.STTProvider, cfg.STTMode = STTProviderParakeet, STTModeOffline
	cfg.ParakeetModel, cfg.ModelsDir, cfg.Language = doc.Model, doc.ModelsDir, doc.Language
	cfg.AcousticNameEnabled = false
	return &cfg, nil
}
