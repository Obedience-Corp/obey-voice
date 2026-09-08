package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type metricsFile struct {
	Scenario           string  `json:"scenario"`
	Profile            string  `json:"profile,omitempty"`
	LiveProvider       string  `json:"live_provider,omitempty"`
	LiveModel          string  `json:"live_model,omitempty"`
	WorkerProvider     string  `json:"worker_provider,omitempty"`
	WorkerModel        string  `json:"worker_model,omitempty"`
	Input              string  `json:"input,omitempty"`
	StopReason         string  `json:"stop_reason,omitempty"`
	JobStarted         int     `json:"job_started"`
	JobFinished        int     `json:"job_finished"`
	JobFailed          int     `json:"job_failed"`
	JobAborted         int     `json:"job_aborted"`
	LiveToolCalls      int     `json:"live_tool_calls"`
	WorkerToolCalls    int     `json:"worker_tool_calls"`
	BoundaryRefusals   int     `json:"boundary_refusals"`
	LiveRouterHit      int     `json:"live_router_hit"`
	LiveRouterMiss     int     `json:"live_router_miss"`
	SpokenLeakLines    int     `json:"spoken_leak_lines"`
	CrackleFailingWavs *int    `json:"crackle_failing_wavs,omitempty"`
	PlaybackWavs       int     `json:"playback_wavs,omitempty"`
	Turns              int     `json:"turns"`
	WallS              float64 `json:"wall_s"`
}

func (e *engine) wallSeconds() float64 {
	if e.started.IsZero() {
		return 0
	}
	return time.Since(e.started).Seconds()
}

func writeReport(e *engine) error {
	if e == nil || e.scn == nil {
		return nil
	}
	m := e.metrics()
	var b strings.Builder
	fmt.Fprintf(&b, "# convoflow %s\n\n", e.scn.Name)
	fmt.Fprintf(&b, "%s\n\n", e.scn.Description)
	fmt.Fprintf(&b, "- run dir: `%s`\n", e.runDir)
	if e.profile.LiveProvider != "" {
		fmt.Fprintf(&b, "- live: %s\n", describeBrain(e.profile.LiveProvider, e.profile.LiveModel, e.profile.LiveHost))
		fmt.Fprintf(&b, "- worker: %s\n", describeBrain(e.profile.WorkerProvider, e.profile.WorkerModel, e.profile.WorkerHost))
		fmt.Fprintf(&b, "- profile: %s · input: %s · router_tools: %s · tts: %s\n", e.profile.Profile, e.profile.Input, e.profile.RouterTools, e.profile.TTSProvider)
	}
	if e.stopReason != "" {
		fmt.Fprintf(&b, "- stop_reason: %s (%.1fs)\n", e.stopReason, e.wallSeconds())
	}
	fmt.Fprintf(&b, "- turns: %d\n", len(e.scn.Turns))
	fmt.Fprintf(&b, "- events: %d\n", len(e.rec.snapshot()))
	fmt.Fprintf(&b, "- job_started: %d · job_finished: %d · job_failed: %d · job_aborted: %d\n", m.JobStarted, m.JobFinished, m.JobFailed, m.JobAborted)
	fmt.Fprintf(&b, "- live_tool_calls: %d · worker_tool_calls: %d · boundary_refusals: %d · live_router_hit: %d · live_router_miss: %d\n", m.LiveToolCalls, m.WorkerToolCalls, m.BoundaryRefusals, m.LiveRouterHit, m.LiveRouterMiss)
	fmt.Fprintf(&b, "- spoken_leak_lines: %d\n", m.SpokenLeakLines)
	if m.crackleKnown {
		fmt.Fprintf(&b, "- crackle_failing_wavs: %d / %d scored\n", m.CrackleFailingWavs, m.PlaybackWavs)
	}
	if len(e.turns) > 0 {
		fmt.Fprintf(&b, "\n## turns\n\n| turn | user | route | spoken | result |\n|---|---|---|---|---|\n")
		for _, tr := range e.turns {
			result := "ok"
			if tr.Err != "" {
				result = "FAIL: " + tr.Err
			}
			fmt.Fprintf(&b, "| %s | %s | %s | %s | %s |\n", tr.ID, cell(tr.User), cell(tr.Route), cell(tr.Spoken), cell(result))
		}
	}
	if e.player != nil {
		fmt.Fprintf(&b, "\n## audio\n\n")
		for _, a := range e.player.analyses() {
			status := "ok"
			if a.HasCrackle {
				status = "FAIL"
			}
			fmt.Fprintf(&b, "- `%s` duration=%.2fs crackle=%s impulses=%d zero_holes=%d longest_zero_hole=%.0fms clipped=%t pauses=%d sample_rate=%d\n",
				a.Path, a.DurationS, status, a.ImpulseCount, a.ZeroHoleRuns, a.LongestZeroHole, a.Clipped, a.MidSilenceRuns, a.SampleRate)
		}
		if len(e.player.analyses()) == 0 {
			fmt.Fprintf(&b, "- (no playback wavs)\n")
		}
	}
	if err := os.WriteFile(filepath.Join(e.runDir, "report.md"), []byte(b.String()), 0o644); err != nil {
		return err
	}
	return writeMetricsJSON(e, m)
}

func describeBrain(provider, model, host string) string {
	s := provider
	if model != "" {
		s += " " + model
	}
	if host != "" {
		s += " @ " + host
	}
	return s
}

func cell(s string) string {
	s = strings.ReplaceAll(s, "|", "\\|")
	return strings.ReplaceAll(strings.TrimSpace(s), "\n", " ")
}

func writeMetricsJSON(e *engine, m runMetrics) error {
	out := metricsFile{
		Scenario:         e.scn.Name,
		Profile:          e.profile.Profile,
		LiveProvider:     e.profile.LiveProvider,
		LiveModel:        e.profile.LiveModel,
		WorkerProvider:   e.profile.WorkerProvider,
		WorkerModel:      e.profile.WorkerModel,
		Input:            e.profile.Input,
		StopReason:       e.stopReason,
		JobStarted:       m.JobStarted,
		JobFinished:      m.JobFinished,
		JobFailed:        m.JobFailed,
		JobAborted:       m.JobAborted,
		LiveToolCalls:    m.LiveToolCalls,
		WorkerToolCalls:  m.WorkerToolCalls,
		BoundaryRefusals: m.BoundaryRefusals,
		LiveRouterHit:    m.LiveRouterHit,
		LiveRouterMiss:   m.LiveRouterMiss,
		SpokenLeakLines:  m.SpokenLeakLines,
		PlaybackWavs:     m.PlaybackWavs,
		Turns:            len(e.scn.Turns),
		WallS:            e.wallSeconds(),
	}
	if m.crackleKnown {
		n := m.CrackleFailingWavs
		out.CrackleFailingWavs = &n
	}
	data, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(filepath.Join(e.runDir, "metrics.json"), data, 0o644)
}
