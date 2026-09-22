//go:build !integration

package cmd

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

func TestTranscribeClientExampleFixtures(t *testing.T) {
	script, fixtures := clientExamplePaths(t)
	cases := []struct {
		name    string
		file    string
		fake    int
		want    int
		wantOut string
		wantErr string
	}{
		{"eof speech", "eof_speech.jsonl", 0, 0, "Here is the recording.", ""},
		{"no speech", "no_speech.jsonl", 0, 0, "(no speech", ""},
		{"missing model", "missing_model.jsonl", 3, 3, "", "model_missing"},
		{"cancelled after accepted", "cancelled.jsonl", 130, 130, "", "cancelled"},
		{"failed at seq 0", "bad_version.jsonl", 2, 2, "", "unsupported_protocol"},
		{"completed then nonzero exit", "eof_speech.jsonl", 4, 4, "", "exit 4"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stream := readClientFixture(t, fixtures, tc.file)
			out, errOut, status := runClientExample(t, script, stream, tc.fake)
			if status != tc.want {
				t.Fatalf("exit = %d, want %d\nstdout:\n%s\nstderr:\n%s", status, tc.want, out, errOut)
			}
			if tc.wantOut != "" && !strings.Contains(out, tc.wantOut) {
				t.Fatalf("stdout %q does not contain %q", out, tc.wantOut)
			}
			if tc.wantErr != "" && !strings.Contains(errOut, tc.wantErr) {
				t.Fatalf("stderr %q does not contain %q", errOut, tc.wantErr)
			}
		})
	}
}

func TestTranscribeClientExampleAcceptedOrder(t *testing.T) {
	script, fixtures := clientExamplePaths(t)
	eof := clientFixtureLines(t, fixtures, "eof_speech.jsonl")
	noSpeech := clientFixtureLines(t, fixtures, "no_speech.jsonl")

	cases := []struct {
		name   string
		stream string
		want   string
	}{
		{
			name:   "completed before accepted",
			stream: setSeq(t, noSpeech[len(noSpeech)-1], 0) + "\n",
			want:   "completed event before accepted",
		},
		{
			name:   "segment before accepted",
			stream: setSeq(t, eof[1], 0) + "\n",
			want:   "segment event before accepted",
		},
		{
			name:   "progress before accepted",
			stream: setSeq(t, eof[2], 0) + "\n",
			want:   "progress event before accepted",
		},
		{
			name:   "second accepted",
			stream: eof[0] + "\n" + setSeq(t, eof[0], 1) + "\n",
			want:   "a second accepted event",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, errOut, status := runClientExample(t, script, []byte(tc.stream), 0)
			if status != 65 {
				t.Fatalf("exit = %d, want 65\nstdout:\n%s\nstderr:\n%s", status, out, errOut)
			}
			if !strings.Contains(errOut, tc.want) {
				t.Fatalf("stderr %q does not contain %q", errOut, tc.want)
			}
			if strings.Contains(out, "Here is the recording.") || strings.Contains(out, "(no speech") {
				t.Fatalf("client treated a malformed stream as a transcript:\n%s", out)
			}
		})
	}
}

func clientExamplePaths(t *testing.T) (script, fixtures string) {
	t.Helper()
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq is required to run the example client")
	}
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Clean(filepath.Join(wd, "../../.."))
	script = filepath.Join(root, "docs", "transcribe-protocol", "client-example.sh")
	fixtures = filepath.Join(root, "docs", "transcribe-protocol", "fixtures")
	if _, err := os.Stat(script); err != nil {
		t.Fatalf("example client: %v", err)
	}
	return script, fixtures
}

func readClientFixture(t *testing.T, dir, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func clientFixtureLines(t *testing.T, dir, name string) []string {
	t.Helper()
	lines := strings.Split(strings.TrimSuffix(string(readClientFixture(t, dir, name)), "\n"), "\n")
	if len(lines) == 0 || lines[0] == "" {
		t.Fatalf("%s is empty", name)
	}
	return lines
}

var seqField = regexp.MustCompile(`"seq":\d+`)

func setSeq(t *testing.T, line string, seq int) string {
	t.Helper()
	if !seqField.MatchString(line) {
		t.Fatalf("line has no seq: %s", line)
	}
	return seqField.ReplaceAllString(line, fmt.Sprintf(`"seq":%d`, seq))
}

var jobIDField = regexp.MustCompile(`"job_id":"[^"]*"`)

func runClientExample(t *testing.T, script string, stream []byte, fakeExit int) (stdout, stderr string, status int) {
	t.Helper()
	dir := t.TempDir()
	streamPath := filepath.Join(dir, "stream.jsonl")
	rewritten := jobIDField.ReplaceAll(stream, []byte(`"job_id":"JOBID"`))
	if err := os.WriteFile(streamPath, rewritten, 0o600); err != nil {
		t.Fatal(err)
	}
	fake := filepath.Join(dir, "fake")
	body := `#!/bin/sh
jobid=$(jq -r .job_id "$3")
sed "s/JOBID/$jobid/g" "$FAKE_STREAM"
echo "fake: diagnostics" >&2
exit "${FAKE_EXIT:-0}"
`
	if err := os.WriteFile(fake, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	audio := filepath.Join(dir, "audio.wav")
	if err := os.WriteFile(audio, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("sh", script, audio)
	cmd.Env = append(os.Environ(),
		"OBEY_VOICE_BIN="+fake,
		"FAKE_STREAM="+streamPath,
		"FAKE_EXIT="+strconv.Itoa(fakeExit),
	)
	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	err := cmd.Run()
	status = 0
	if err != nil {
		ee, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("run client: %v", err)
		}
		status = ee.ExitCode()
	}
	return out.String(), errBuf.String(), status
}
