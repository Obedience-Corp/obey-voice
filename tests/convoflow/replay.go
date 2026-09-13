package main

import (
	"context"
	"fmt"
	"os"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/stt"
)

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

	vad, err := audio.NewVAD(r.cfg)
	if err != nil {
		return fmt.Errorf("WAV replay %q: VAD: %w", path, err)
	}
	defer vad.Delete()

	recognizer, recognizerCleanup, err := stt.NewProvider(r.cfg, source, vad)
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

	_, err = p.RunTurn(ctx)
	if err != nil {
		return fmt.Errorf("WAV replay %q: turn: %w", path, err)
	}
	return nil
}
