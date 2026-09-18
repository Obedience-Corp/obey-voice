// Package capture writes sealed conversation bundles for Talk/Lab recording.
package capture

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Obedience-Corp/obey-voice/internal/transcript"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

const Schema = "obey-voice.capture/v1"

const (
	ModeOff  = ""
	ModeTalk = "talk"
	ModeLab  = "lab"
)

// Turn is a canonical transcript line written at seal.
type Turn struct {
	Role    string `json:"role"`
	Content string `json:"content"`
	Speaker string `json:"speaker,omitempty"`
}

// Manifest is bundle metadata (obey-voice.capture/v1).
type Manifest struct {
	Schema                string     `json:"schema"`
	Mode                  string     `json:"mode"`
	SessionID             string     `json:"session_id"`
	StartedAt             time.Time  `json:"started_at"`
	SealedAt              *time.Time `json:"sealed_at,omitempty"`
	Cut                   string     `json:"cut"`
	Dir                   string     `json:"dir"`
	DroppedEvents         int64      `json:"dropped_events,omitempty"`
	ExternalDebugAudio    string     `json:"external_debug_audio,omitempty"`
	ExternalTranscriptLog string     `json:"external_transcript_log,omitempty"`
}

// Options configure Start.
type Options struct {
	Root                  string
	SessionID             string
	Mode                  string
	MidTurn               bool
	Bus                   *events.Bus
	Turns                 []Turn
	ExternalDebugAudio    string
	ExternalTranscriptLog string
}

// Session is one open capture bundle.
type Session struct {
	mu       sync.Mutex
	manifest Manifest
	writer   *transcript.Writer
	unsub    func()
	prevLab  string
	labSet   bool
}

// Start opens a captures/<session>-<stamp>/ bundle and taps the event bus.
func Start(opts Options) (*Session, error) {
	mode := strings.TrimSpace(strings.ToLower(opts.Mode))
	if mode != ModeTalk && mode != ModeLab {
		return nil, fmt.Errorf("capture mode %q is not talk or lab", opts.Mode)
	}
	sessionID := strings.TrimSpace(opts.SessionID)
	if sessionID == "" {
		sessionID = "session"
	}
	root := strings.TrimSpace(opts.Root)
	if root == "" {
		root = config.CapturesDir()
	}
	stamp := time.Now().UTC().Format("20060102T150405Z")
	dir := filepath.Join(root, sessionID+"-"+stamp)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create capture dir: %w", err)
	}
	cut := "boundary"
	if opts.MidTurn {
		cut = "mid_turn"
	}
	w, err := transcript.NewWriter(filepath.Join(dir, "events.jsonl"))
	if err != nil {
		return nil, err
	}
	s := &Session{
		manifest: Manifest{
			Schema:                Schema,
			Mode:                  mode,
			SessionID:             sessionID,
			StartedAt:             time.Now().UTC(),
			Cut:                   cut,
			Dir:                   dir,
			ExternalDebugAudio:    strings.TrimSpace(opts.ExternalDebugAudio),
			ExternalTranscriptLog: strings.TrimSpace(opts.ExternalTranscriptLog),
		},
		writer: w,
	}
	s.unsub = w.AttachAll(opts.Bus)
	if err := s.writeManifest(); err != nil {
		_ = s.Stop(nil)
		return nil, err
	}
	if mode == ModeLab {
		if strings.TrimSpace(opts.ExternalDebugAudio) == "" {
			lab := filepath.Join(dir, "lab")
			if err := os.MkdirAll(lab, 0o700); err != nil {
				_ = s.Stop(nil)
				return nil, fmt.Errorf("create lab dir: %w", err)
			}
			s.prevLab = audio.DebugAudioDir()
			if err := audio.SetDebugAudioDir(lab); err != nil {
				_ = s.Stop(nil)
				return nil, err
			}
			s.labSet = true
		}
	}
	return s, nil
}

// Dir is the bundle path.
func (s *Session) Dir() string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.manifest.Dir
}

// Mode is talk or lab.
func (s *Session) Mode() string {
	if s == nil {
		return ModeOff
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.manifest.Mode
}

// Stop seals the bundle.
func (s *Session) Stop(turns []Turn) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.unsub != nil {
		s.unsub()
		s.unsub = nil
	}
	if s.labSet {
		_ = audio.SetDebugAudioDir(s.prevLab)
		s.labSet = false
	}
	if s.writer != nil {
		s.manifest.DroppedEvents = s.writer.Dropped()
		_ = s.writer.Close()
		s.writer = nil
	}
	now := time.Now().UTC()
	s.manifest.SealedAt = &now
	if err := writeTurns(s.manifest.Dir, turns); err != nil {
		return err
	}
	return s.writeManifestLocked()
}

func (s *Session) writeManifest() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.writeManifestLocked()
}

func (s *Session) writeManifestLocked() error {
	path := filepath.Join(s.manifest.Dir, "manifest.json")
	data, err := json.MarshalIndent(s.manifest, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o600)
}

func writeTurns(dir string, turns []Turn) error {
	if turns == nil {
		turns = []Turn{}
	}
	data, err := json.MarshalIndent(turns, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "transcript.json"), append(data, '\n'), 0o600)
}

// List sealed (or open) bundles under root.
func List(root string) ([]Manifest, error) {
	if strings.TrimSpace(root) == "" {
		root = config.CapturesDir()
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []Manifest
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(root, e.Name(), "manifest.json"))
		if err != nil {
			continue
		}
		var m Manifest
		if json.Unmarshal(raw, &m) != nil || m.Schema != Schema {
			continue
		}
		if m.Dir == "" {
			m.Dir = filepath.Join(root, e.Name())
		}
		out = append(out, m)
	}
	return out, nil
}
