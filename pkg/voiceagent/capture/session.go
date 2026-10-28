// Package capture writes sealed conversation bundles for Talk/Lab recording.
package capture

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
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
	RuntimeID             string            `json:"runtime_id"`
	ConversationEpoch     string            `json:"conversation_epoch"`
	CaptureID             string            `json:"capture_id"`
	TraceSchema           string            `json:"trace_schema"`
	Health                transcript.Health `json:"health"`
	Status                string            `json:"status"`
	Schema                string            `json:"schema"`
	Mode                  string            `json:"mode"`
	SessionID             string            `json:"session_id"`
	StartedAt             time.Time         `json:"started_at"`
	SealedAt              *time.Time        `json:"sealed_at,omitempty"`
	Cut                   string            `json:"cut"`
	Dir                   string            `json:"dir"`
	DroppedEvents         int64             `json:"dropped_events,omitempty"`
	ExternalDebugAudio    string            `json:"external_debug_audio,omitempty"`
	ExternalTranscriptLog string            `json:"external_transcript_log,omitempty"`
}

// Options configure Start.
type Options struct {
	Trace                 events.TraceContext
	Environment           map[string]string
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
	persistManifest func(Manifest) error
	persistTurns    func(string, []Turn) error
	sealOnce        sync.Once
	sealDone        chan struct{}
	sealErr         error
	mu              sync.Mutex
	manifest        Manifest
	writer          *transcript.Writer
	unsub           func()
	prevLab         string
	labSet          bool
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
	stamp := time.Now().UTC().Format("20060102T150405Z") + "-" + rand.Text()
	prefix := sessionID
	if len(prefix) > 64 {
		prefix = prefix[:64]
	}
	prefix = strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			return r
		}
		return '_'
	}, prefix)
	dir := filepath.Join(root, prefix+"-"+stamp)
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("create capture root: %w", err)
	}
	if err := os.Mkdir(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create capture dir: %w", err)
	}
	cut := "boundary"
	if opts.MidTurn {
		cut = "mid_turn"
	}
	captureID := rand.Text()
	trace := opts.Trace
	if trace.SessionID == "" {
		trace.SessionID = sessionID
	}
	environment := map[string]string{"platform": runtime.GOOS, "architecture": runtime.GOARCH}
	for k, v := range opts.Environment {
		switch k {
		case "brain_provider", "brain_model", "stt_provider", "stt_mode", "stt_model", "tts_provider", "tts_model", "tts_tier", "build_version", "build_revision", "build_modified":
		default:
			continue
		}
		if len(environment) >= 16 {
			break
		}
		if len(k) <= 64 && len(v) <= 256 {
			environment[k] = v
		}
	}
	w, err := transcript.NewCaptureWriter(filepath.Join(dir, "events.jsonl"), transcript.CaptureOptions{CaptureID: captureID, Trace: trace, ContentPolicy: mode, Cut: cut, Environment: environment})
	if err != nil {
		return nil, err
	}
	s := &Session{
		persistManifest: writeManifestValue, persistTurns: writeTurns,
		sealDone: make(chan struct{}),
		manifest: Manifest{
			RuntimeID: trace.RuntimeID, ConversationEpoch: trace.ConversationEpoch, CaptureID: captureID, TraceSchema: events.TraceSchema, Status: "open",
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
	s.unsub = w.Observe(opts.Bus)
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
	return s.manifest.Dir
}

// Mode is talk or lab.
func (s *Session) Mode() string {
	if s == nil {
		return ModeOff
	}
	return s.manifest.Mode
}

// SealAsync detaches and rejects late callbacks immediately; one goroutine owns
// draining, file sync and the final atomic manifest. A stuck disk never retries.
func (s *Session) SealAsync(turns []Turn) <-chan struct{} {
	if s == nil {
		done := make(chan struct{})
		close(done)
		return done
	}
	s.sealOnce.Do(func() {
		s.mu.Lock()
		if s.unsub != nil {
			s.unsub()
			s.unsub = nil
		}
		if s.labSet {
			_ = audio.SetDebugAudioDir(s.prevLab)
			s.labSet = false
		}
		w := s.writer
		w.StopAccepting()
		s.manifest.Status = "sealing"
		s.mu.Unlock()
		go func() {
			defer close(s.sealDone)
			err := w.Close()
			health := w.Health()
			if err == nil && health.WriteErrors > 0 {
				err = fmt.Errorf("capture has %d write errors", health.WriteErrors)
			}
			if err == nil && health.Limited {
				err = fmt.Errorf("capture reached %d byte limit", transcript.MaxCaptureBytes)
			}
			if err == nil {
				err = s.persistTurns(s.manifest.Dir, turns)
			}
			s.mu.Lock()
			s.manifest.Health = health
			s.manifest.DroppedEvents = health.Dropped
			s.manifest.Status = "incomplete"
			if err == nil {
				now := time.Now().UTC()
				s.manifest.SealedAt = &now
				s.manifest.Status = "sealed"
			}
			manifest := s.manifest
			s.mu.Unlock()
			if manifestErr := s.persistManifest(manifest); err == nil {
				err = manifestErr
			}
			s.mu.Lock()
			s.sealErr = err
			if err != nil {
				s.manifest.Status = "incomplete"
				s.manifest.SealedAt = nil
			}
			s.mu.Unlock()
		}()
	})
	return s.sealDone
}
func (s *Session) Stop(turns []Turn) error {
	if s == nil {
		return nil
	}
	<-s.SealAsync(turns)
	return s.SealError()
}
func (s *Session) Done() <-chan struct{}     { return s.sealDone }
func (s *Session) SealError() error          { s.mu.Lock(); defer s.mu.Unlock(); return s.sealErr }
func (s *Session) Health() transcript.Health { return s.writer.Health() }

func (s *Session) writeManifest() error {
	s.mu.Lock()
	manifest := s.manifest
	s.mu.Unlock()
	return writeManifestValue(manifest)
}

func writeManifestValue(manifest Manifest) error {
	path := filepath.Join(manifest.Dir, "manifest.json")
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
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

func (s *Session) Matches(trace events.TraceContext) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.manifest.RuntimeID == "" || (s.manifest.RuntimeID == trace.RuntimeID && s.manifest.ConversationEpoch == trace.ConversationEpoch && s.manifest.SessionID == trace.SessionID)
}

func (s *Session) ID() string         { return s.manifest.CaptureID }
func (s *Session) EventsPath() string { return filepath.Join(s.manifest.Dir, "events.jsonl") }
