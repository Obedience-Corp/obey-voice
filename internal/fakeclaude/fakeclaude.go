// Package fakeclaude writes a stand-in claude CLI for tests that list models
// and must never start the real one.
package fakeclaude

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Mode is how the stand-in answers the initialize control request.
type Mode string

const (
	// Answer replies with the models given to OnPath.
	Answer Mode = "answer"
	// TooOld exits before answering, as a CLI without the control protocol does.
	TooOld Mode = "old"
	// Refuse replies with a control error.
	Refuse Mode = "refuse"
	// Hang reads the request and never answers.
	Hang Mode = "hang"
)

// Models is a model list shaped like the one the Claude CLI reports.
const Models = `[` +
	`{"value":"default","displayName":"Default (recommended)","description":"Use the default model"},` +
	`{"value":"sonnet","displayName":"Sonnet","description":"Everyday tasks"},` +
	`{"value":"opus","displayName":"Opus","description":"Complex work"},` +
	`{"value":"haiku","displayName":"Haiku","description":"Quick answers"},` +
	`{"value":"sonnet[1m]","displayName":"Sonnet (1M context)","description":"Long sessions"}` +
	`]`

// script uses only shell builtins, so the stand-in can be alone on PATH.
const script = `#!/bin/sh
pwd -P >> {{RUNS}}
case {{MODE}} in
old) echo "error: unknown option '--input-format'" >&2; exit 1 ;;
esac
IFS= read -r line
case {{MODE}} in
hang) while IFS= read -r line; do :; done; exit 0 ;;
esac
id=${line#*'"request_id":"'}
id=${id%%'"'*}
case {{MODE}} in
refuse) printf '{"type":"control_response","response":{"subtype":"error","request_id":"%s","error":"Not logged in"}}\n' "$id" ;;
*) printf '{"type":"control_response","response":{"subtype":"success","request_id":"%s","response":{"models":%s}}}\n' "$id" {{MODELS}} ;;
esac
while IFS= read -r line; do :; done
`

// CLI is a stand-in claude CLI on PATH.
type CLI struct {
	runs string
}

// OnPath makes the stand-in the only program on PATH for the rest of the test.
// models is the JSON array it reports in Answer mode.
func OnPath(t testing.TB, mode Mode, models string) CLI {
	t.Helper()
	dir := t.TempDir()
	cli := CLI{runs: filepath.Join(dir, "runs")}
	body := strings.NewReplacer(
		"{{RUNS}}", shellQuote(cli.runs),
		"{{MODE}}", string(mode),
		"{{MODELS}}", shellQuote(models),
	).Replace(script)
	bin := filepath.Join(dir, "bin")
	if err := os.Mkdir(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "claude"), []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	return cli
}

// Runs returns the working directory of each run so far, in order.
func (c CLI) Runs(t testing.TB) []string {
	t.Helper()
	data, err := os.ReadFile(c.runs)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
