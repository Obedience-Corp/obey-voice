package main

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/stt"
)

// decodeRecorder is enabled only by the isolated scenario's stt_decodes flag.
// It runs after inference on the STT goroutine. Its disk overhead disqualifies
// these runs from timing populations; ordinary runs never install an observer.
type decodeRecorder struct {
	mu    sync.Mutex
	dir   string
	count int
	err   error
}

type decodeEvidence struct {
	Format      string    `json:"format"`
	SampleRate  int       `json:"sample_rate"`
	Samples     int       `json:"samples"`
	PCM         string    `json:"pcm"`
	SHA256      string    `json:"sha256"`
	WAV         string    `json:"wav"`
	WAVSHA256   string    `json:"wav_sha256"`
	Transcript  string    `json:"transcript"`
	DecodeError string    `json:"decode_error,omitempty"`
	StartedAt   time.Time `json:"started_at"`
	CompletedAt time.Time `json:"completed_at"`
}

func newDecodeRecorder(dir string) (*decodeRecorder, error) {
	if err := os.MkdirAll(filepath.Dir(dir), 0o700); err != nil {
		return nil, fmt.Errorf("decoder evidence parent: %w", err)
	}
	if err := os.Mkdir(dir, 0o700); err != nil {
		return nil, fmt.Errorf("new decoder evidence directory: %w", err)
	}
	return &decodeRecorder{dir: dir}, nil
}

func (r *decodeRecorder) observe(ctx context.Context, observation stt.DecodeObservation) (err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return r.err
	}
	defer func() { r.err = errors.Join(r.err, err) }()
	if err := ctx.Err(); err != nil {
		return err
	}
	// Persist failed decoder results while the observation context is active.
	data := make([]byte, 4*len(observation.Samples))
	for i, value := range observation.Samples {
		binary.LittleEndian.PutUint32(data[i*4:], math.Float32bits(value))
	}
	base := fmt.Sprintf("decode-%03d", r.count+1)
	pcmName, wavName := base+".f32le", base+".wav"
	if err := os.WriteFile(filepath.Join(r.dir, pcmName), data, 0o600); err != nil {
		return fmt.Errorf("write exact decoder PCM: %w", err)
	}
	if err := audio.WriteWAVFloat32(filepath.Join(r.dir, wavName), audio.SampleRate, observation.Samples); err != nil {
		return fmt.Errorf("write decoder WAV: %w", err)
	}
	wav, err := os.ReadFile(filepath.Join(r.dir, wavName))
	if err != nil {
		return fmt.Errorf("fingerprint decoder WAV: %w", err)
	}
	evidence := decodeEvidence{Format: "float32-little-endian-mono", SampleRate: audio.SampleRate,
		Samples: len(observation.Samples), PCM: pcmName, SHA256: fmt.Sprintf("%x", sha256.Sum256(data)),
		WAV: wavName, WAVSHA256: fmt.Sprintf("%x", sha256.Sum256(wav)), Transcript: observation.Text,
		StartedAt: observation.StartedAt, CompletedAt: observation.CompletedAt}
	if observation.Err != nil {
		evidence.DecodeError = observation.Err.Error()
	}
	metadata, err := json.MarshalIndent(evidence, "", "  ")
	if err != nil {
		return fmt.Errorf("encode decoder evidence: %w", err)
	}
	if err := os.WriteFile(filepath.Join(r.dir, base+".json"), append(metadata, '\n'), 0o600); err != nil {
		return fmt.Errorf("write decoder evidence: %w", err)
	}
	r.count++
	return nil
}

func (r *decodeRecorder) verify() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return r.err
	}
	if r.count == 0 {
		return errors.New("no offline decoder evidence recorded; this run cannot validate decoder input (streaming STT is unsupported)")
	}
	return nil
}
