package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type metricsFile struct {
	Scenario           string `json:"scenario"`
	JobStarted         int    `json:"job_started"`
	LiveToolCalls      int    `json:"live_tool_calls"`
	SpokenLeakLines    int    `json:"spoken_leak_lines"`
	CrackleFailingWavs *int   `json:"crackle_failing_wavs,omitempty"`
	PlaybackWavs       int    `json:"playback_wavs,omitempty"`
	Turns              int    `json:"turns"`
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
	fmt.Fprintf(&b, "- turns: %d\n", len(e.scn.Turns))
	fmt.Fprintf(&b, "- events: %d\n", len(e.rec.snapshot()))
	fmt.Fprintf(&b, "- job_started: %d\n", m.JobStarted)
	fmt.Fprintf(&b, "- live_tool_calls: %d\n", m.LiveToolCalls)
	fmt.Fprintf(&b, "- spoken_leak_lines: %d\n", m.SpokenLeakLines)
	if m.crackleKnown {
		fmt.Fprintf(&b, "- crackle_failing_wavs: %d / %d scored\n", m.CrackleFailingWavs, m.PlaybackWavs)
	}
	if e.player != nil {
		fmt.Fprintf(&b, "\n## audio\n\n")
		for _, a := range e.player.analyses() {
			status := "ok"
			if a.HasCrackle {
				status = "FAIL"
			}
			fmt.Fprintf(&b, "- `%s` duration=%.2fs crackle=%s impulses=%d mid_silence_runs=%d sample_rate=%d\n",
				a.Path, a.DurationS, status, a.ImpulseCount, a.MidSilenceRuns, a.SampleRate)
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

func writeMetricsJSON(e *engine, m runMetrics) error {
	out := metricsFile{
		Scenario:        e.scn.Name,
		JobStarted:      m.JobStarted,
		LiveToolCalls:   m.LiveToolCalls,
		SpokenLeakLines: m.SpokenLeakLines,
		PlaybackWavs:    m.PlaybackWavs,
		Turns:           len(e.scn.Turns),
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
