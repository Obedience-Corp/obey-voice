package brain

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/ollama/ollama/api"
)

// sameOutputShown is the run of identical rounds at which a worker's repeated
// output is reported. Two alike in a row is ordinary.
const sameOutputShown = 3

// SameOutputNote is how a run of identical tool output reads wherever it is
// shown. It is empty for a run too short to report.
func SameOutputNote(rounds int) string {
	if rounds < sameOutputShown {
		return ""
	}
	return fmt.Sprintf("same output for %d rounds", rounds)
}

// sameOutput counts a worker's consecutive tool rounds whose tools returned
// the same output, whatever calls produced it. It only reports: a job that
// polls an unchanged status looks the same as one going in circles, so the
// count is left for the person or the persona to judge.
type sameOutput struct {
	report func(int)
	last   string
	rounds int
	shown  bool
}

func newSameOutput(worker bool, report func(int)) *sameOutput {
	if !worker || report == nil {
		return nil
	}
	return &sameOutput{report: report}
}

// observe records a completed round. report receives the run length each time
// it grows at or past sameOutputShown, and 0 once when a reported run ends.
func (s *sameOutput) observe(ctx context.Context, calls []api.ToolCall, results []string) {
	if s == nil || len(calls) == 0 || len(results) != len(calls) || ctx.Err() != nil {
		return
	}
	sig := outputSignature(calls, results)
	if sig == s.last {
		s.rounds++
	} else {
		s.last, s.rounds = sig, 1
	}
	switch {
	case s.rounds >= sameOutputShown:
		s.shown = true
		s.report(s.rounds)
	case s.shown:
		s.shown = false
		s.report(0)
	}
}

// outputSignature hashes each tool's name with what it returned, leaving out
// the arguments, in an order that does not depend on call order.
func outputSignature(calls []api.ToolCall, results []string) string {
	parts := make([]string, len(calls))
	for i, c := range calls {
		sum := sha256.Sum256([]byte(toolOutput(c.Function.Name, results[i])))
		parts[i] = c.Function.Name + "\x00" + string(sum[:])
	}
	slices.Sort(parts)
	return strings.Join(parts, "\x01")
}

// toolOutput drops the command run_command echoes back, so a reworded command
// with the same output is the same output.
func toolOutput(name, result string) string {
	if name != "run_command" {
		return result
	}
	var decoded struct {
		Output *string `json:"output"`
	}
	if json.Unmarshal([]byte(result), &decoded) != nil || decoded.Output == nil {
		return result
	}
	return *decoded.Output
}
