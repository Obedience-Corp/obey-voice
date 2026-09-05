package main

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
)

// convoflowCrackle is looser than the CI Studio Display defaults. Neural TTS
// is not callback-sized device audio.
var convoflowCrackle = audio.CrackleThresholds{
	MaxImpulses:         8,
	MaxMidSilenceRuns:   2,
	MaxMidSilenceFrames: audio.SampleRate / 10, // 100ms of holes at capture rate
}

const analysisSilenceAbs = 1.0 / 512.0

type wavAnalysis struct {
	Path             string  `json:"path"`
	SampleRate       int     `json:"sample_rate"`
	SampleCount      int     `json:"sample_count"`
	DurationS        float64 `json:"duration_s"`
	RMS              float64 `json:"rms"`
	Peak             float64 `json:"peak"`
	LeadingSilenceS  float64 `json:"leading_silence_s"`
	TrailingSilenceS float64 `json:"trailing_silence_s"`
	ImpulseCount     int     `json:"impulse_count"`
	MidSilenceRuns   int     `json:"mid_silence_runs"`
	MidSilenceFrames int     `json:"mid_silence_frames"`
	MaxAbsDelta      float64 `json:"max_abs_delta"`
	P99AbsDelta      float64 `json:"p99_abs_delta"`
	HasCrackle       bool    `json:"has_crackle"`
}

func scoreSamples(path string, samples []float32, rate int) wavAnalysis {
	a := wavAnalysis{
		Path:        path,
		SampleRate:  rate,
		SampleCount: len(samples),
	}
	if rate > 0 {
		a.DurationS = float64(len(samples)) / float64(rate)
	}
	a.RMS, a.Peak = rmsPeak(samples)
	a.LeadingSilenceS, a.TrailingSilenceS = edgeSilence(samples, rate, analysisSilenceAbs)
	m := audio.AnalyzeFloat32(samples, convoflowCrackle)
	a.ImpulseCount = m.ImpulseCount
	a.MidSilenceRuns = m.MidSilenceRuns
	a.MidSilenceFrames = m.MidSilenceFrames
	a.MaxAbsDelta = m.MaxAbsDelta
	a.P99AbsDelta = m.P99AbsDelta
	a.HasCrackle = m.HasCrackle(convoflowCrackle)
	return a
}

func scoreWAVFile(path string) (wavAnalysis, error) {
	samples, rate, err := audio.ReadWAVFloat32(path)
	if err != nil {
		return wavAnalysis{}, fmt.Errorf("read %s: %w", path, err)
	}
	return scoreSamples(path, samples, rate), nil
}

func writeAnalysis(path string, a wavAnalysis) error {
	data, err := json.MarshalIndent(a, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(path, data, 0o644)
}

func scoreRunDir(runDir string) ([]wavAnalysis, error) {
	if _, err := os.Stat(runDir); err != nil {
		return nil, fmt.Errorf("analyze %s: %w", runDir, err)
	}
	var out []wavAnalysis
	audioDir := filepath.Join(runDir, "audio")
	err := filepath.WalkDir(audioDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if d.IsDir() || d.Name() != "playback.wav" {
			return nil
		}
		a, err := scoreWAVFile(path)
		if err != nil {
			return err
		}
		if rel, relErr := filepath.Rel(runDir, path); relErr == nil {
			a.Path = rel
		}
		if err := writeAnalysis(filepath.Join(filepath.Dir(path), "analysis.json"), a); err != nil {
			return err
		}
		out = append(out, a)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func analyzeRunDir(runDir string) error {
	scores, err := scoreRunDir(runDir)
	if err != nil {
		return err
	}
	failing := 0
	fmt.Printf("convoflow analyze %s\n", runDir)
	if len(scores) == 0 {
		fmt.Printf("  (no playback.wav files)\n")
		return nil
	}
	for _, a := range scores {
		status := "ok"
		if a.HasCrackle {
			status = "FAIL"
			failing++
		}
		fmt.Printf("  %s  %.2fs  crackle=%s  impulses=%d  mid_silence_runs=%d\n",
			a.Path, a.DurationS, status, a.ImpulseCount, a.MidSilenceRuns)
	}
	fmt.Printf("  %d failing / %d wav\n", failing, len(scores))
	if failing > 0 {
		return fmt.Errorf("%d wav(s) failed crackle", failing)
	}
	return nil
}

func rmsPeak(samples []float32) (rms, peak float64) {
	if len(samples) == 0 {
		return 0, 0
	}
	var sum float64
	for _, s := range samples {
		v := math.Abs(float64(s))
		if v > peak {
			peak = v
		}
		sum += float64(s) * float64(s)
	}
	return math.Sqrt(sum / float64(len(samples))), peak
}

func edgeSilence(samples []float32, rate int, silenceAbs float64) (lead, trail float64) {
	if rate <= 0 || len(samples) == 0 {
		return 0, 0
	}
	i := 0
	for i < len(samples) && math.Abs(float64(samples[i])) < silenceAbs {
		i++
	}
	j := len(samples) - 1
	for j >= i && math.Abs(float64(samples[j])) < silenceAbs {
		j--
	}
	lead = float64(i) / float64(rate)
	trail = float64(len(samples)-1-j) / float64(rate)
	if trail < 0 {
		trail = 0
	}
	return lead, trail
}
