package chatmedia

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Obedience-Corp/obey-voice/internal/testmedia"
)

func TestReplyImportsOnlyExplicitOwnedImages(t *testing.T) {
	root := t.TempDir()
	store := Store{Dir: filepath.Join(t.TempDir(), "assets")}
	path := filepath.Join(root, "result with spaces.gif")
	if err := os.WriteFile(path, testmedia.GIF(), 0600); err != nil {
		t.Fatal(err)
	}
	text, refs, err := store.ImportReply("Here is the result.\n![Animation](<"+path+">)", root)
	if err != nil || len(refs) != 1 || text != "Here is the result.\nAnimation" {
		t.Fatalf("%q %+v %v", text, refs, err)
	}
	if err = os.Remove(path); err != nil {
		t.Fatal(err)
	}
	preview, err := store.Preview(refs[0])
	if err != nil || len(preview.Frames) != 6 {
		t.Fatalf("durable GIF: %+v %v", preview, err)
	}
	for _, input := range []string{path, "Example: ![x](" + path + ")", "```markdown\n![x](" + path + ")\n```"} {
		_, refs, err = store.ImportReply(input, root)
		if err != nil || len(refs) != 0 {
			t.Fatalf("imported ordinary text: %q", input)
		}
	}
}
func TestReplyRejectsOutsideRemoteAndSymlinkFiles(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "secret.png")
	if err := os.WriteFile(outside, testmedia.PNG(), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link.png")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	store := Store{Dir: t.TempDir()}
	for _, path := range []string{outside, link, "https://example.com/image.png", filepath.Join(root, "missing.png")} {
		text, refs, err := store.ImportReply("![Result](<"+path+">)", root)
		if err == nil || len(refs) != 0 || !strings.Contains(text, "Image unavailable") {
			t.Fatalf("%s: %q %+v %v", path, text, refs, err)
		}
	}
}
func TestReplyImportBoundsAndProtectsExamples(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "result.png")
	_ = os.WriteFile(path, testmedia.PNG(), 0600)
	store := Store{Dir: t.TempDir()}
	line := "![Result](<" + path + ">)"
	_, refs, err := store.ImportReply(strings.Repeat(line+"\n", 5), root)
	if err == nil || len(refs) != 1 {
		t.Fatalf("limit/dedup: %+v %v", refs, err)
	}
	protected := ProtectImageExamples("```\n" + line + "\n```")
	_, refs, err = store.ImportReply(strings.ReplaceAll(protected, "```", ""), root)
	if err != nil || len(refs) != 0 {
		t.Fatal("cleanup activated fenced example")
	}
}
