package audio

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

var debugAudioConfig struct {
	sync.RWMutex
	dir       string
	textWrite sync.Mutex
}

// SetDebugAudioDir enables playback capture for players created afterwards.
// An empty path disables capture. The recorder creates one timestamped child
// directory per Player so concurrent Obey Voice processes never overwrite data.
func SetDebugAudioDir(dir string) error {
	if dir != "" {
		abs, err := filepath.Abs(dir)
		if err != nil {
			return fmt.Errorf("resolve audio debug directory: %w", err)
		}
		if err := os.MkdirAll(abs, 0o700); err != nil {
			return fmt.Errorf("create audio debug directory: %w", err)
		}
		dir = abs
	}
	debugAudioConfig.Lock()
	debugAudioConfig.dir = dir
	debugAudioConfig.Unlock()
	return nil
}

func debugAudioDir() string {
	debugAudioConfig.RLock()
	defer debugAudioConfig.RUnlock()
	return debugAudioConfig.dir
}

// DebugAudioDir returns the active capture root, or an empty string when
// capture is disabled.
func DebugAudioDir() string {
	return debugAudioDir()
}

type debugSynthesisMetadata struct {
	CreatedAt time.Time `json:"created_at"`
	PID       int       `json:"pid"`
	Provider  string    `json:"provider"`
	Original  string    `json:"original_text"`
	Prepared  string    `json:"prepared_text"`
}

// RecordDebugSynthesis stores the exact text entering a TTS provider and any
// provider-specific prepared form. It is intentionally called off the audio
// callback thread.
func RecordDebugSynthesis(provider, original, prepared string) {
	dir := debugAudioDir()
	if dir == "" {
		return
	}
	event, err := json.Marshal(debugSynthesisMetadata{
		CreatedAt: time.Now(),
		PID:       os.Getpid(),
		Provider:  provider,
		Original:  original,
		Prepared:  prepared,
	})
	if err != nil {
		return
	}

	debugAudioConfig.textWrite.Lock()
	defer debugAudioConfig.textWrite.Unlock()
	file, err := os.OpenFile(filepath.Join(dir, "syntheses.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	_, _ = file.Write(append(event, '\n'))
	_ = file.Close()
}
