package brain

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

const commandOutputLimit = 16 * 1024

// commandOutput keeps a bounded prefix while counting every output byte.
// os/exec serializes writes when stdout and stderr share the same writer.
type commandOutput struct {
	prefix []byte
	total  int64
}

func (b *commandOutput) Write(p []byte) (int, error) {
	n := len(p)
	b.total += int64(n)
	remaining := commandOutputLimit - len(b.prefix)
	if remaining > len(p) {
		remaining = len(p)
	}
	if remaining > 0 {
		b.prefix = append(b.prefix, p[:remaining]...)
	}
	return n, nil
}

// commandText preserves valid UTF-8 at a byte boundary. Invalid binary output
// is represented by replacement characters before entering text diagnostics.
func commandText(raw []byte, truncated bool) string {
	if truncated {
		for len(raw) > 0 && !utf8.Valid(raw) {
			// Only remove an incomplete final rune; invalid bytes within output are
			// retained for ToValidUTF8's replacement below.
			start := len(raw) - 1
			for start > 0 && !utf8.RuneStart(raw[start]) {
				start--
			}
			if utf8.FullRune(raw[start:]) {
				break
			}
			raw = raw[:start]
		}
	}
	return strings.ToValidUTF8(string(raw), "�")
}

func toolRunCommandWithTimeout(ctx context.Context, workDir string, args map[string]any, timeout time.Duration) string {
	result, _ := toolRunCommandWithTimeoutOutcome(ctx, workDir, args, timeout)
	return result
}

func toolRunCommandWithTimeoutOutcome(ctx context.Context, workDir string, args map[string]any, timeout time.Duration) (string, *events.ToolOutcome) {
	begun := time.Now()
	command, _ := args["command"].(string)
	outcome := &events.ToolOutcome{InputSummary: redactToolText(command, 80), OutputPreview: "(empty)"}
	if strings.TrimSpace(command) == "" {
		outcome.Status = events.ToolOutcomeFailed
		outcome.Reason = "command is required"
		outcome.Elapsed = time.Since(begun)
		return "error: " + outcome.Reason, outcome
	}
	ctx, cancel := context.WithTimeout(ctx, clampCommandTimeout(timeout))
	defer cancel()
	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	cmd.Dir = workDir
	configureCommandCancellation(cmd)
	cmd.WaitDelay = 100 * time.Millisecond
	var output commandOutput
	cmd.Stdout, cmd.Stderr = &output, &output
	err := cmd.Run()
	outcome.Elapsed = time.Since(begun)
	outcome.OutputBytes = output.total
	if cmd.ProcessState != nil {
		if code := cmd.ProcessState.ExitCode(); code >= 0 {
			outcome.ExitCode = intPtr(code)
		}
		outcome.Signal = commandSignal(cmd.ProcessState)
	}
	outcome.Status = events.ToolOutcomeSucceeded
	if err != nil {
		outcome.Status = events.ToolOutcomeFailed
		outcome.Reason = redactToolText(err.Error(), 240)
		if ctx.Err() == context.DeadlineExceeded {
			outcome.Status = events.ToolOutcomeTimedOut
			outcome.Reason = "command timed out"
		} else if ctx.Err() == context.Canceled {
			outcome.Status = events.ToolOutcomeCanceled
			outcome.Reason = "command canceled"
		}
	}
	capturedTruncated := output.total > int64(len(output.prefix))
	raw := commandText(output.prefix, capturedTruncated)
	redacted := redactToolText(raw, 0)
	trimmed := strings.TrimSpace(redacted)
	preview := toolResultPreview(redacted)
	outcome.OutputPreview = preview
	outcome.Truncated = capturedTruncated || strings.Contains(trimmed, "\n") || utf8.RuneCountInString(trimmed) > 240
	// Keep the existing model-facing JSON and failure detail. Diagnostics use
	// the observed output above, never reinterpret arbitrary provider JSON.
	result := raw
	if len(result) > commandOutputLimit {
		result = commandText([]byte(result[:commandOutputLimit]), true)
		capturedTruncated = true
	}
	if capturedTruncated {
		result += "\n... (truncated)"
	}
	// Keep failure visible to the model even when the output prefix was capped.
	if err != nil {
		result += fmt.Sprintf("\nexit error: %s", redactToolText(err.Error(), 240))
	}
	response, _ := json.Marshal(map[string]string{"output": result, "command": command})
	return string(response), outcome
}

func intPtr(v int) *int { return &v }
