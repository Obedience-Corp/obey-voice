package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/pipeline"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/stt"
)

type wavVADFactory func(*config.Config) (pipeline.VoiceDetector, func(), error)
type wavSTTFactory func(*config.Config, *audio.FixtureSource, pipeline.VoiceDetector) (stt.Provider, func(), error)

func defaultWAVVAD(cfg *config.Config) (pipeline.VoiceDetector, func(), error) {
	v, err := audio.NewVAD(cfg)
	if err != nil {
		return nil, nil, err
	}
	return v, v.Delete, nil
}

func defaultWAVSTT(cfg *config.Config, source *audio.FixtureSource, vad pipeline.VoiceDetector) (stt.Provider, func(), error) {
	// NewProvider currently requires the concrete local VAD. Keep the default
	// path type-safe while allowing tests to inject a lightweight detector/STT.
	concrete, ok := vad.(*audio.VAD)
	if !ok {
		return nil, nil, fmt.Errorf("WAV replay: default STT requires local VAD")
	}
	return stt.NewProvider(cfg, source, concrete)
}

// runWAV replays one finite recording through the already-constructed
// pipeline. The agent, brain and work coordinator remain the same instance,
// so speech replay has the same history and canonical worker state as a text
// turn. The fixture source is deliberately local and never opens a capture
// device.
func (r *liveRunner) runWAV(ctx context.Context, path string) error {
	if ctx == nil {
		return fmt.Errorf("WAV replay: nil context")
	}
	if r == nil || r.agent == nil || r.agent.Pipeline == nil {
		return fmt.Errorf("WAV replay: agent is not configured")
	}
	if r.cfg == nil {
		return fmt.Errorf("WAV replay: config is not configured")
	}
	if path == "" {
		return fmt.Errorf("WAV replay: path is empty")
	}
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("WAV replay %q: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("WAV replay %q: not a regular file", path)
	}

	source, err := audio.NewFixtureSourceFromWAV(path, audio.ChunkSize, true)
	if err != nil {
		return fmt.Errorf("WAV replay %q: source: %w", path, err)
	}
	defer source.Close()

	vadFactory := r.wavVADFactory
	if vadFactory == nil {
		vadFactory = defaultWAVVAD
	}
	vad, vadCleanup, err := vadFactory(r.cfg)
	if err != nil {
		return fmt.Errorf("WAV replay %q: VAD: %w", path, err)
	}
	if vadCleanup != nil {
		defer vadCleanup()
	}

	sttFactory := r.wavSTTFactory
	if sttFactory == nil {
		sttFactory = defaultWAVSTT
	}
	recognizer, recognizerCleanup, err := sttFactory(r.cfg, source, vad)
	if err != nil {
		return fmt.Errorf("WAV replay %q: STT: %w", path, err)
	}
	if recognizerCleanup != nil {
		defer recognizerCleanup()
	}

	p := r.agent.Pipeline
	oldSTT, oldVAD, oldUtterance := p.STT, p.VAD, p.Utterance
	p.STT = recognizer
	p.VAD = vad
	p.Utterance = nil
	defer func() {
		p.STT, p.VAD, p.Utterance = oldSTT, oldVAD, oldUtterance
	}()

	text, err := p.RunTurn(ctx)
	if err != nil {
		return fmt.Errorf("WAV replay %q: turn: %w", path, err)
	}
	if strings.TrimSpace(text) == "" {
		return fmt.Errorf("WAV replay %q: turn produced no speech transcript", path)
	}
	return nil
}
