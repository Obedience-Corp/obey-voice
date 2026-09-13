//go:build !integration

package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

func printBenchmarkSummary(results []benchmarkResult) {
	fmt.Println()
	fmt.Printf("  %s\n", titleStyle.Render("Obey Voice Benchmark"))
	fmt.Println()

	for _, result := range results {
		switch result.Mode {
		case "stt":
			fmt.Printf("  %s %s\n", sectionStyle.Render(fmt.Sprintf("[%d]", result.Iteration)), fmt.Sprintf("%s :: %s", result.Provider, filepath.Base(result.Fixture)))
			printMetric("stt final", formatMetric(result.Metrics.STTFinalElapsed))
			if result.Expected != "" {
				printMetric("transcript score", fmt.Sprintf("%.2f", result.TranscriptScore))
			}
			if result.Transcript != "" {
				printMetric("transcript", result.Transcript)
			}
		default:
			// Full-turn results carry a fixture instead of a prompt.
			header := result.Prompt
			if header == "" && result.Fixture != "" {
				header = filepath.Base(result.Fixture)
				if result.Provider != "" {
					header = fmt.Sprintf("%s :: %s", result.Provider, header)
				}
			}
			fmt.Printf("  %s %s\n", sectionStyle.Render(fmt.Sprintf("[%d]", result.Iteration)), header)
			printMetric("total", result.Elapsed.Round(time.Millisecond).String())
			if result.Metrics.STTFinalElapsed > 0 {
				printMetric("stt final", formatMetric(result.Metrics.STTFinalElapsed))
			}
			if result.Transcript != "" {
				printMetric("transcript", result.Transcript)
			}
			if result.Expected != "" {
				printMetric("transcript score", fmt.Sprintf("%.2f", result.TranscriptScore))
			}
			printMetric("first model chunk", formatMetric(result.Metrics.FirstModelChunkElapsed))
			printMetric("model complete", formatMetric(result.Metrics.ModelCompleteElapsed))
			printMetric("first segment", formatMetric(result.Metrics.FirstSegmentElapsed))
			printMetric("first audio ready", formatMetric(result.Metrics.FirstAudioReadyElapsed))
			printMetric("playback start", formatMetric(result.Metrics.PlaybackStartElapsed))
			if result.Metrics.PlaybackCompleteElapsed > 0 {
				printMetric("playback complete", formatMetric(result.Metrics.PlaybackCompleteElapsed))
			}
			if result.Metrics.Interrupted || result.Metrics.BargeInElapsed > 0 {
				printMetric("interruption", formatMetric(result.Metrics.BargeInElapsed))
			}
			printTTSSummary(result)
		}

		if len(result.Errors) > 0 {
			for _, err := range result.Errors {
				stage := err.Stage
				if stage == "" {
					stage = "runtime"
				}
				fmt.Printf("    %s %s\n", failStyle.Render(fmt.Sprintf("error [%s]:", stage)), err.Message)
			}
		}
		// Reported, but not in the style that says the run failed: it did not.
		for _, entry := range result.Recovered {
			fmt.Printf("    %s %s\n", dimStyle.Render(fmt.Sprintf("degraded [%s]:", entry.Stage)), entry.Message)
		}
		if len(result.Violations) > 0 {
			for _, violation := range result.Violations {
				fmt.Printf("    %s %s\n", failStyle.Render("violation:"), violation)
			}
		}
		fmt.Println()
	}
}

// printTTSSummary reports what synthesized the turn and what it cost per
// segment. The milestones above describe only the first segment, so a reply
// whose later sentences are the slow part reads as fast without this.
func printTTSSummary(result benchmarkResult) {
	m := result.Metrics
	if m.TTSProvider == "" && len(m.Segments) == 0 {
		return
	}

	engine := m.TTSProvider
	if engine == "" {
		engine = "unknown"
	}
	if m.TTSTier != "" {
		engine += " " + m.TTSTier
	}
	// Name the requested provider too when it differs: a leg that fell back
	// is not a measurement of what was asked for. Checked before the topology
	// is appended, so decorating the line cannot invent a substitution.
	if want := result.TTSRequested; want != "" && want != m.TTSProvider && want != engine {
		engine = fmt.Sprintf("%s (requested %s)", engine, want)
	}
	printMetric("tts", engine)
	// Topology is its own line: the same engine name means different silicon
	// and a socket in the path once speech runs off-box, and a worker that
	// does not stream cannot produce first audio before synthesis ends.
	if m.TTSPlace != "" {
		place := m.TTSPlace
		if m.TTSWorkerStreaming {
			place += ", worker streams PCM"
		}
		printMetric("speech place", place)
	}

	if m.StallWatchdogFired {
		printMetric("stall watchdog", failStyle.Render(fmt.Sprintf("FIRED after %s", formatMetric(m.FirstAudioGrace))))
	} else if m.FirstAudioGrace > 0 {
		printMetric("stall grace", formatMetric(m.FirstAudioGrace))
	}
	// Dead air is the headline for a whole-utterance engine: the first-audio
	// delay is what you notice, but the silence between sentences is the same
	// defect continuing for the rest of the reply.
	if m.DeadAirElapsed > 0 {
		detail := formatMetric(m.DeadAirElapsed)
		if speaking := m.SpeakingWindow(); speaking > 0 {
			detail += fmt.Sprintf(" of %s speaking (%.0f%%)", formatMetric(speaking),
				float64(m.DeadAirElapsed)/float64(speaking)*100)
		}
		printMetric("dead air", detail)
	}
	if m.TTSFallbacks > 0 {
		printMetric("tts fallbacks", failStyle.Render(fmt.Sprintf("%d segment(s) spoken by the fallback", m.TTSFallbacks)))
	}

	var engineTotal time.Duration
	var audio float64
	for _, seg := range m.Segments {
		engineTotal += seg.EngineElapsed
		audio += seg.AudioSeconds
	}
	if len(m.Segments) > 0 {
		summary := fmt.Sprintf("%d, engine %s for %.2fs audio", len(m.Segments), formatMetric(engineTotal), audio)
		if audio > 0 {
			summary += fmt.Sprintf(" (rtf %.2f)", engineTotal.Seconds()/audio)
		}
		printMetric("segments", summary)
	}
	for _, seg := range m.Segments {
		// Engine and queue are printed apart because a large wait on a later
		// segment is the previous sentence still playing, not a slow engine.
		detail := fmt.Sprintf("engine %s / audio %.2fs", formatMetric(seg.EngineElapsed), seg.AudioSeconds)
		if rtf := seg.RealtimeFactor(); rtf > 0 {
			detail += fmt.Sprintf(" = rtf %.2f", rtf)
		}
		if wait := seg.QueueElapsed(); wait > 0 {
			detail += fmt.Sprintf(", queued %s", formatMetric(wait))
		}
		if seg.Fallback {
			detail += " [fallback]"
		}
		if seg.Interrupted {
			detail += " [interrupted]"
		}
		printMetric(fmt.Sprintf("  segment %d", seg.Index), detail)
	}
}

// printMetric renders one indented "label: value" benchmark metric line with a
// dimmed label.
func printMetric(label, value string) {
	fmt.Printf("    %s %s\n", dimStyle.Render(label+":"), value)
}

func writeBenchmarkJSON(path string, results []benchmarkResult) error {
	data, err := json.MarshalIndent(results, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal benchmark results: %w", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write benchmark results: %w", err)
	}
	return nil
}

func formatMetric(d time.Duration) string {
	if d <= 0 {
		return "n/a"
	}
	return d.Round(time.Millisecond).String()
}
