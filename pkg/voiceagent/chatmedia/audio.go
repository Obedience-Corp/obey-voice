package chatmedia

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math"
	"sync"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/utterance"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/wav"
)

// CanonicalAudio accepts only bounded persona PCM16 input. It preserves every
// sample bit while removing optional WAV chunks from the model transport.
func CanonicalAudio(data []byte) (utterance.AudioRef, []byte, error) {
	if len(data) < 44 || len(data) > utterance.MaxBytes || uint64(binary.LittleEndian.Uint32(data[4:8]))+8 != uint64(len(data)) {
		return utterance.AudioRef{}, nil, utterance.ErrInvalidAudio
	}
	// Inspect the fmt declaration as well as the sample layout; malformed byte
	// rates and block alignment must not become a different canonical clip.
	var haveFmt, haveData bool
	for pos := 12; pos < len(data); {
		if len(data)-pos < 8 {
			return utterance.AudioRef{}, nil, utterance.ErrInvalidAudio
		}
		size := uint64(binary.LittleEndian.Uint32(data[pos+4:]))
		end := uint64(pos) + 8 + size
		if end+size%2 > uint64(len(data)) {
			return utterance.AudioRef{}, nil, utterance.ErrInvalidAudio
		}
		switch string(data[pos : pos+4]) {
		case "data":
			if haveData {
				return utterance.AudioRef{}, nil, utterance.ErrInvalidAudio
			}
			haveData = true
		case "fmt ":
			if haveFmt {
				return utterance.AudioRef{}, nil, utterance.ErrInvalidAudio
			}
			haveFmt = true
			if size < 16 || binary.LittleEndian.Uint32(data[pos+16:]) != utterance.SampleRate*2 || binary.LittleEndian.Uint16(data[pos+20:]) != 2 {
				return utterance.AudioRef{}, nil, utterance.ErrInvalidAudio
			}
		}
		pos = int(end + size%2)
	}
	format, err := wav.ReadWAVFormat(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return utterance.AudioRef{}, nil, fmt.Errorf("inspect utterance WAV: %w: %w", utterance.ErrInvalidAudio, err)
	}
	ref := utterance.AudioRef{MIME: "audio/wav", SampleRate: format.SampleRate, Channels: format.Channels, SampleCount: format.Frames()}
	// Validate format/count before allocating the canonical payload.
	ref.ID = string(bytes.Repeat([]byte{'0'}, 64))
	if err := ref.Validate(); err != nil {
		return utterance.AudioRef{}, nil, err
	}
	pcm := data[format.DataOffset : format.DataOffset+format.DataBytes]
	out := canonicalPCM(pcm)
	sum := sha256.Sum256(out)
	ref.ID = hex.EncodeToString(sum[:])
	return ref, out, nil
}

func canonicalPCM(pcm []byte) []byte {
	out := make([]byte, 44+len(pcm))
	copy(out, "RIFF")
	binary.LittleEndian.PutUint32(out[4:], uint32(len(out)-8))
	copy(out[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(out[16:], 16)
	binary.LittleEndian.PutUint16(out[20:], 1)
	binary.LittleEndian.PutUint16(out[22:], 1)
	binary.LittleEndian.PutUint32(out[24:], utterance.SampleRate)
	binary.LittleEndian.PutUint32(out[28:], utterance.SampleRate*2)
	binary.LittleEndian.PutUint16(out[32:], 2)
	binary.LittleEndian.PutUint16(out[34:], 16)
	copy(out[36:], "data")
	binary.LittleEndian.PutUint32(out[40:], uint32(len(pcm)))
	copy(out[44:], pcm)
	return out
}

// ImportSamples encodes a captured persona turn once. Nonfinite input and
// overflow are rejected before allocating or accepting a different waveform.
func (s *AudioSession) ImportSamples(ctx context.Context, samples []float32) (utterance.AudioRef, error) {
	if err := ctx.Err(); err != nil {
		return utterance.AudioRef{}, err
	}
	if len(samples) == 0 || len(samples) > utterance.MaxSamples {
		return utterance.AudioRef{}, utterance.ErrInvalidAudio
	}
	pcm := make([]byte, len(samples)*2)
	for i, sample := range samples {
		if math.IsNaN(float64(sample)) || math.IsInf(float64(sample), 0) {
			return utterance.AudioRef{}, utterance.ErrInvalidAudio
		}
		value := int16(max(-1, min(1, sample)) * math.MaxInt16)
		binary.LittleEndian.PutUint16(pcm[i*2:], uint16(value))
	}
	return s.Import(ctx, canonicalPCM(pcm))
}

const SessionAudioBudget = 32 << 20

// AudioSession owns ephemeral, bounded audio for one conversation. Reference
// counts represent canonical turns and in-flight consumers. Close discards all
// bytes; this store never creates files or implicitly reads durable history.
type AudioSession struct {
	mu      sync.Mutex
	entries map[string]*audioEntry
	used    int
	closed  bool
}
type audioEntry struct {
	ref   utterance.AudioRef
	data  []byte
	holds int
}

func NewAudioSession() *AudioSession { return &AudioSession{entries: make(map[string]*audioEntry)} }
func (s *AudioSession) Import(ctx context.Context, data []byte) (utterance.AudioRef, error) {
	if err := ctx.Err(); err != nil {
		return utterance.AudioRef{}, err
	}
	ref, canonical, err := CanonicalAudio(data)
	if err != nil {
		return utterance.AudioRef{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return utterance.AudioRef{}, err
	}
	if s.closed {
		return utterance.AudioRef{}, utterance.ErrAudioUnavailable
	}
	if entry := s.entries[ref.ID]; entry != nil {
		entry.holds++
		return ref, nil
	}
	if len(canonical) > SessionAudioBudget-s.used {
		return utterance.AudioRef{}, utterance.ErrBudgetExceeded
	}
	s.entries[ref.ID] = &audioEntry{ref: ref, data: canonical, holds: 1}
	s.used += len(canonical)
	return ref, nil
}
func (s *AudioSession) Retain(ref utterance.AudioRef) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry := s.entries[ref.ID]
	if s.closed || entry == nil || entry.ref != ref {
		return utterance.ErrAudioUnavailable
	}
	entry.holds++
	return nil
}
func (s *AudioSession) Release(ref utterance.AudioRef) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry := s.entries[ref.ID]
	if entry == nil || entry.ref != ref {
		return
	}
	entry.holds--
	if entry.holds == 0 {
		s.used -= len(entry.data)
		clear(entry.data)
		delete(s.entries, ref.ID)
	}
}
func (s *AudioSession) ResolveAudio(ctx context.Context, ref utterance.AudioRef) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := ref.Validate(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	entry := s.entries[ref.ID]
	if s.closed || entry == nil || entry.ref != ref {
		return nil, utterance.ErrAudioUnavailable
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return bytes.Clone(entry.data), nil
}
func (s *AudioSession) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, entry := range s.entries {
		clear(entry.data)
		delete(s.entries, id)
	}
	s.used = 0
	s.closed = true
}
