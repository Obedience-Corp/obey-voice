package transcription

import (
	"os/exec"
	"strings"
	"testing"
)

const pkgPath = "github.com/Obedience-Corp/obey-voice/pkg/voiceagent/transcription"

// TestPackageImportsOnlyAudioAndSTT is the compiler-checked half of "no
// conversational side effects": a package that cannot reach the brain, TTS, the
// pipeline, the serve API or meeting filing cannot speak, answer or file
// anything, whatever a future edit does inside it.
func TestPackageImportsOnlyAudioAndSTT(t *testing.T) {
	allowed := map[string]bool{
		"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio": true,
		"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/stt":   true,
	}
	for _, imp := range goList(t, "-f", "{{range .Imports}}{{.}}\n{{end}}", pkgPath) {
		if strings.HasPrefix(imp, "github.com/Obedience-Corp/obey-voice/") && !allowed[imp] {
			t.Errorf("%s imports %s; this package may import only audio and stt", pkgPath, imp)
		}
	}
}

// TestPackageCannotReachTheConversationalStack checks the whole build graph for
// the packages that carry side effects. The brain is not on this list: the
// configured STT correction chain has an optional local-model stage, so
// pkg/voiceagent/stt imports brain itself and every consumer inherits it.
func TestPackageCannotReachTheConversationalStack(t *testing.T) {
	forbidden := []string{
		"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/tts",
		"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/pipeline",
		"github.com/Obedience-Corp/obey-voice/internal/netapi",
		"github.com/Obedience-Corp/obey-voice/internal/meeting",
		"github.com/Obedience-Corp/obey-voice/internal/listen",
	}
	deps := goList(t, "-deps", pkgPath)
	for _, dep := range deps {
		for _, bad := range forbidden {
			if dep == bad || strings.HasPrefix(dep, bad+"/") {
				t.Errorf("%s depends on %s", pkgPath, dep)
			}
		}
	}
}

// goList runs go list and returns its non-empty output lines. It skips rather
// than fails when the toolchain is not on PATH, so the suite still runs inside
// a stripped container.
func goList(t *testing.T, args ...string) []string {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go is not on PATH, so the import graph cannot be inspected")
	}
	out, err := exec.Command("go", append([]string{"list"}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("go list %v: %v\n%s", args, err, out)
	}
	var lines []string
	for line := range strings.SplitSeq(string(out), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			lines = append(lines, line)
		}
	}
	if len(lines) == 0 {
		t.Fatalf("go list %v returned no output; the import graph was not inspected", args)
	}
	return lines
}
