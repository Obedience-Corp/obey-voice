package brain

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

// fence is a boundary at home/campaigns/demo with a fake home, so the
// tests never depend on the real one.
func fence(t *testing.T) (Boundary, string) {
	t.Helper()
	home := t.TempDir()
	root := filepath.Join(home, "campaigns", "demo")
	if err := os.MkdirAll(filepath.Join(root, "notes"), 0o755); err != nil {
		t.Fatal(err)
	}
	return Boundary{Root: root, home: home}, home
}

func TestBoundaryPathsStayUnderTheRoot(t *testing.T) {
	b, home := fence(t)
	work := filepath.Join(b.Root, "notes")
	ok := []string{".", "README.md", "notes/todo.md", b.Root, filepath.Join(b.Root, "a", "b.txt"), "../README.md"}
	for _, p := range ok {
		if err := b.CheckPath(work, p); err != nil {
			t.Errorf("CheckPath(%q) = %v, want inside", p, err)
		}
	}
	refused := []string{"../../other/x", "/etc/hosts", filepath.Join(home, ".grok", "sessions"), filepath.Join(home, "campaigns", "other", "README.md"), "../.."}
	for _, p := range refused {
		err := b.CheckPath(work, p)
		if err == nil || !strings.Contains(err.Error(), "outside the campaign boundary") {
			t.Errorf("CheckPath(%q) = %v, want refused", p, err)
		}
	}
}

func TestBoundaryCommandsRefuseHomeOutsideTheRootAndTheRegistry(t *testing.T) {
	b, home := fence(t)
	other := filepath.Join(home, "campaigns", "other")
	ok := []string{
		"ls",
		"ls /usr/bin",
		"go build ./...",
		"git commit -m 'notes'",
		"git -C notes log",
		"echo a > notes/../out.txt",
		"cat /etc/hosts | head",
		"camp status",
		"/opt/homebrew/bin/go version",
	}
	for _, c := range ok {
		if err := b.CheckCommand(b.Root, c); err != nil {
			t.Errorf("CheckCommand(%q) = %v, want allowed", c, err)
		}
	}
	refused := []string{
		"cat ~/.grok/sessions/x/chat_history.jsonl",
		"ls ~",
		"cat $HOME/.claude/projects/x",
		"cat ${HOME}/.obey/campaign/registry.json",
		"cd " + other + " && git commit -m x",
		"git -C ../other status",
		"git -C=" + other + " log",
		"GIT_DIR=" + other + "/.git git log",
		"camp list --json",
		"/opt/homebrew/bin/camp list",
		"cd .. && ls",
	}
	for _, c := range refused {
		if err := b.CheckCommand(b.Root, c); err == nil {
			t.Errorf("CheckCommand(%q) = nil, want refused", c)
		}
	}
}

func TestBoundaryOffChecksNothing(t *testing.T) {
	var b Boundary
	if b.Enabled() || b.CheckPath("/x", "/etc/hosts") != nil || b.CheckCommand("/x", "camp list") != nil {
		t.Fatal("an empty boundary must not refuse anything")
	}
	if NewBoundary("  ").Enabled() {
		t.Fatal("blank root must be no boundary")
	}
}

func TestBoundaryFollowsASymlinkedRoot(t *testing.T) {
	home := t.TempDir()
	real := filepath.Join(home, "real-campaign")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(home, "campaign-link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlink: %v", err)
	}
	if err := os.WriteFile(filepath.Join(real, "a.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	b := Boundary{Root: link, home: home}
	if err := b.CheckPath(link, filepath.Join(real, "a.txt")); err != nil {
		t.Fatalf("real path under a symlinked root refused: %v", err)
	}
}

func TestBoundaryContextNamesTheRoot(t *testing.T) {
	got := BoundaryContext("/camp/root")
	for _, want := range []string{"Boundary:", "stays under /camp/root", "camp list", "git commit or git push outside /camp/root"} {
		if !strings.Contains(got, want) {
			t.Errorf("boundary context lacks %q:\n%s", want, got)
		}
	}
}

func TestWorkerDeniedToolsOnlyForAFencedWorker(t *testing.T) {
	if got := workerDeniedTools(nil); got != nil {
		t.Fatalf("nil cfg = %v, want nil", got)
	}
	if got := workerDeniedTools(&config.Config{}); got != nil {
		t.Fatalf("live brain = %v, want nil", got)
	}
	got := workerDeniedTools(&config.Config{WorkerBoundary: "/camp"})
	if len(got) == 0 || !strings.Contains(strings.Join(got, " "), "camp list") {
		t.Fatalf("fenced worker = %v, want camp list denied", got)
	}
}
