package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// NoticesFile is where one-time notices are recorded once shown. It sits
// next to config.yaml rather than in it, so showing a notice never rewrites
// the user's settings.
func NoticesFile() string {
	return filepath.Join(ConfigDir(), "notices.json")
}

// Notices is a file of one-time notices already shown, keyed by name.
type Notices struct {
	path  string
	mu    sync.Mutex
	shown map[string]bool
}

// NewNotices reads the notices recorded at path; a missing or unreadable
// file is an empty record.
func NewNotices(path string) *Notices {
	n := &Notices{path: path, shown: map[string]bool{}}
	for key := range readNotices(path) {
		n.shown[key] = true
	}
	return n
}

// Shown reports whether the notice was already shown, here or by another
// process sharing the file.
func (n *Notices) Shown(key string) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	if !n.shown[key] && n.path != "" {
		if _, ok := readNotices(n.path)[key]; ok {
			n.shown[key] = true
		}
	}
	return n.shown[key]
}

// Mark records the notice as shown, here and in the file.
func (n *Notices) Mark(key string) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.shown[key] = true
	if n.path == "" {
		return nil
	}
	record := readNotices(n.path)
	if _, ok := record[key]; ok {
		return nil
	}
	record[key] = time.Now().UTC().Format(time.RFC3339)
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(n.path), 0o755); err != nil {
		return fmt.Errorf("record notice: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(n.path), ".notices-*.json")
	if err != nil {
		return fmt.Errorf("record notice: %w", err)
	}
	_, werr := tmp.Write(append(data, '\n'))
	cerr := tmp.Close()
	if err := errors.Join(werr, cerr); err != nil {
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("record notice: %w", err)
	}
	if err := os.Rename(tmp.Name(), n.path); err != nil {
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("record notice: %w", err)
	}
	return nil
}

func readNotices(path string) map[string]string {
	record := map[string]string{}
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &record)
	}
	if record == nil {
		record = map[string]string{}
	}
	return record
}
