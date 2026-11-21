package chatmedia

import (
	"fmt"
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
	for _, input := range []string{path, "```markdown\n![x](" + path + ")\n```", "Write `![x](<" + path + ">)` on its own line."} {
		_, refs, err = store.ImportReply(input, root)
		if err != nil || len(refs) != 0 {
			t.Fatalf("imported ordinary text: %q", input)
		}
	}
	if err = os.WriteFile(path, testmedia.GIF(), 0600); err != nil {
		t.Fatal(err)
	}
	// A reference that ends its line is a picture directive, not an example.
	text, refs, err = store.ImportReply("Example: ![x](<"+path+">)", root)
	if err != nil || len(refs) != 1 || text != "Example:\nx" {
		t.Fatalf("trailing reference: %q %+v %v", text, refs, err)
	}
}

func writePictures(t *testing.T, root string, n int) []string {
	t.Helper()
	paths := make([]string, n)
	for i := range paths {
		paths[i] = filepath.Join(root, fmt.Sprintf("picture %d.png", i))
		if err := os.WriteFile(paths[i], testmedia.SolidPNG(uint8(40*i)), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return paths
}

func TestReplyImportsReferencesThatEndTheLine(t *testing.T) {
	root := t.TempDir()
	store := Store{Dir: t.TempDir()}
	p := writePictures(t, root, 3)
	for _, tc := range []struct {
		reply, want string
		images      int
	}{
		{"One more for you, a closer look. ![Blue bird](" + p[0] + ")", "One more for you, a closer look.\nBlue bird", 1},
		{"Here they are, freshly made. ![A](" + p[0] + ") ![B](" + p[1] + ")", "Here they are, freshly made.\nA\nB", 2},
		{"Fresh pictures, here they are. ![A](" + p[0] + ") ![B](" + p[1] + ") ![C](" + p[2] + ")", "Fresh pictures, here they are.\nA\nB\nC", 3},
		{"Here it is: ![Chart](<" + p[0] + ">)", "Here it is:\nChart", 1},
		{"Here: ![the `main` chart](<" + p[0] + ">)", "Here:\nthe `main` chart", 1},
		{"Text. ![A](<" + p[0] + ">)\r\nnext", "Text.\r\nA\r\nnext", 1},
		{"Intro\r\n![A](<" + p[0] + ">)\r\nnext", "Intro\r\nA\r\nnext", 1},
		{"![A](" + p[0] + ") ![B](" + p[1] + ")", "A\nB", 2},
		{"First the chart.\n![A](<" + p[0] + ">)\nThen the other two. ![B](<" + p[1] + ">) ![C](<" + p[2] + ">)\nThat is all.", "First the chart.\nA\nThen the other two.\nB\nC\nThat is all.", 3},
	} {
		text, refs, err := store.ImportReply(tc.reply, root)
		if err != nil || len(refs) != tc.images || text != tc.want {
			t.Errorf("%q: got %q, %d images, %v", tc.reply, text, len(refs), err)
		}
		if err := store.CheckReplyFiles(tc.reply, root); err != nil {
			t.Errorf("%q: check: %v", tc.reply, err)
		}
	}
}

// Bullets beside pictures are dropped. A number may be the answer, so it stays.
func TestListOfPicturesKeepsOnlyMeaningfulMarkers(t *testing.T) {
	root := t.TempDir()
	store := Store{Dir: t.TempDir()}
	p := writePictures(t, root, 2)
	for list, want := range map[string]string{
		"- ![A](" + p[0] + ")\n- ![B](" + p[1] + ")":       "A\nB",
		"1. ![A](<" + p[0] + ">)\n2. ![B](<" + p[1] + ">)": "1.\nA\n2.\nB",
		"Answer:\n42. ![chart](<" + p[0] + ">)":            "Answer:\n42.\nchart",
	} {
		text, refs, err := store.ImportReply(list, root)
		if err != nil || len(refs) != strings.Count(list, "![") || text != want {
			t.Errorf("%q: got %q, %d images, %v", list, text, len(refs), err)
		}
	}
}

// A reference followed by other text on its line is a mention, as before.
func TestMentionedReferenceIsLeftAsWritten(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.png")
	if err := os.WriteFile(outside, testmedia.PNG(), 0600); err != nil {
		t.Fatal(err)
	}
	store := Store{Dir: t.TempDir()}
	p := writePictures(t, root, 2)
	a, b := "<"+p[0]+">", "<"+p[1]+">"
	for _, text := range []string{
		"Here ![Chart](" + a + ") it is.",
		"Here it is. ![Blue bird](<" + filepath.Join(root, "missing.png") + ">) Enjoy.",
		"Here it is. ![Blue bird](<" + outside + ">) Enjoy.",
		"Here it is. ![Blue bird](https://example.com/image.png) Enjoy.",
		"Here's the picture: ![cat](" + a + "). Let me know!",
		"Here are both: ![A](" + a + ") and ![B](" + b + ").",
		"I drew ![a cat](" + a + ") for you.",
		"(see ![a](" + a + "))",
		"| A | ![A](" + a + ") |",
		"[![Build](https://example.com/b.svg)](https://example.com/ci)",
		"![a](" + a + "), ![b](" + b + ").",
		"Here it is: ![cat](" + a + ").",
		"Look ![a]![z](" + a + ")(" + b + ") ok",
	} {
		got, refs, err := store.ImportReply(text, root)
		if got != text || len(refs) != 0 || err != nil || store.CheckReplyFiles(text, root) != nil {
			t.Errorf("%q: %q %d %v", text, got, len(refs), err)
		}
		if SeparateImageReferences(text) != text || ReplyProse(text) != text {
			t.Errorf("%q: lifted a mention", text)
		}
	}
}

func TestTrailingReferenceFailsLikeAWholeLineOne(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.png")
	if err := os.WriteFile(outside, testmedia.PNG(), 0600); err != nil {
		t.Fatal(err)
	}
	store := Store{Dir: t.TempDir()}
	for _, path := range []string{filepath.Join(root, "missing.png"), outside, "https://example.com/image.png"} {
		text, refs, err := store.ImportReply("Here it is. ![Blue bird](<"+path+">)", root)
		if err == nil || len(refs) != 0 || text != "Here it is.\n[Image unavailable: Blue bird]" {
			t.Errorf("%s: %q %+v %v", path, text, refs, err)
		}
		_, _, whole := store.ImportReply("Here it is.\n![Blue bird](<"+path+">)", root)
		if whole == nil || err.Error() != whole.Error() {
			t.Errorf("%s: trailing error %v, whole-line error %v", path, err, whole)
		}
		if store.CheckReplyFiles("Here it is. ![Blue bird](<"+path+">)", root) == nil {
			t.Errorf("%s: check accepted an unusable reference", path)
		}
	}
}

func TestTrailingReferencesShareTheLimitAndDeduplicate(t *testing.T) {
	root := t.TempDir()
	store := Store{Dir: t.TempDir()}
	p := writePictures(t, root, 5)
	var line strings.Builder
	line.WriteString("All of them.")
	for _, path := range p {
		line.WriteString(" ![Chart](" + path + ")")
	}
	_, refs, err := store.ImportReply(line.String(), root)
	if err == nil || len(refs) != MaxAttachments {
		t.Fatalf("limit: %d images, %v", len(refs), err)
	}
	same := "Twice. ![A](" + p[0] + ") ![B](" + p[0] + ")"
	if _, refs, err = store.ImportReply(same, root); err != nil || len(refs) != 1 {
		t.Fatalf("dedup: %d images, %v", len(refs), err)
	}
}

func TestSeparateImageReferences(t *testing.T) {
	for in, want := range map[string]string{
		"Sentence. ![A](/w/a.png) ![B](/w/b.png)":  "Sentence.\n![A](/w/a.png)\n![B](/w/b.png)",
		"Here ![A](/w/a.png) it is.":               "Here ![A](/w/a.png) it is.",
		"Here: ![the `main` chart](/w/a.png)":      "Here:\n![the `main` chart](/w/a.png)",
		"Text. ![A](/w/a.png)\r":                   "Text.\r\n![A](/w/a.png)\r",
		"Intro\r\n![A](/w/a.png)\r\nnext":          "Intro\r\n![A](/w/a.png)\r\nnext",
		"Look ![a]![z](</w/a.png>)(</w/b.png>) ok": "Look ![a]![z](</w/a.png>)(</w/b.png>) ok",
		"![A](/w/a.png) ![B](/w/b.png)":            "![A](/w/a.png)\n![B](/w/b.png)",
		"Look: ![A](</w/with space.png>)":          "Look:\n![A](</w/with space.png>)",
		"Look: ![A](/w/with space.png)":            "Look:\n![A](/w/with space.png)",
		"  ![A](/w/a.png)  ":                       "  ![A](/w/a.png)  ",
		"```\nSee ![A](/w/a.png)\n```":             "```\nSee ![A](/w/a.png)\n```",
		"~~~\n![A](/w/a.png) ![B](/w/b.png)\n~~~":  "~~~\n![A](/w/a.png) ![B](/w/b.png)\n~~~",
		"Write `![A](/w/a.png)` like this.":        "Write `![A](/w/a.png)` like this.",
		"Write ``![A](/w/a.png)`` here.":           "Write ``![A](/w/a.png)`` here.",
		"Unfinished ![A](/w/a":                     "Unfinished ![A](/w/a",
		"- ![A](/w/a.png)\n- ![B](/w/b.png)":       "![A](/w/a.png)\n![B](/w/b.png)",
		"1. ![A](/w/a.png)\n2) ![B](/w/b.png)":     "1.\n![A](/w/a.png)\n2)\n![B](/w/b.png)",
		"> ![A](/w/a.png)":                         "![A](/w/a.png)",
		"- Here it is ![A](/w/a.png)":              "- Here it is\n![A](/w/a.png)",
	} {
		got := SeparateImageReferences(in)
		if got != want {
			t.Errorf("%q: got %q, want %q", in, got, want)
		}
		if again := SeparateImageReferences(got); again != got {
			t.Errorf("%q: not idempotent: %q", in, again)
		}
	}
	for _, text := range []string{"  Plain prose with trailing space.  \n\n\tIndented line\r\n", "Use the ! [x] (y) form.", "A path /w/a.png in prose."} {
		if got := SeparateImageReferences(text); got != text {
			t.Errorf("changed text without a reference: %q -> %q", text, got)
		}
		if got := ReplyProse(text); got != strings.TrimSpace(text) {
			t.Errorf("ReplyProse changed text without a reference: %q -> %q", text, got)
		}
	}
	if got := ReplyProse("Here they are. ![A](/w/a.png) ![B](/w/b.png)"); got != "Here they are." {
		t.Errorf("ReplyProse = %q", got)
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
	for _, example := range []string{
		"```\n" + line + "\n```",
		"```\nExample: " + line + " " + line + "\n```",
		"Write ```" + line + "``` on its own line.",
	} {
		protected := ProtectImageExamples(example)
		_, refs, err = store.ImportReply(strings.ReplaceAll(protected, "```", ""), root)
		if err != nil || len(refs) != 0 {
			t.Fatalf("cleanup activated an example: %q", protected)
		}
	}
}

func TestReplyCandidatesOnlyOffersDecodableWorkspaceFiles(t *testing.T) {
	root := t.TempDir()
	for name, data := range map[string][]byte{"cat.jpg": []byte("File not found"), "cat2.jpg": []byte("<html>Error</html>"), "photo.png": testmedia.PNG(), ".private.png": testmedia.PNG()} {
		if err := os.WriteFile(filepath.Join(root, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	external := filepath.Join(t.TempDir(), "outside.png")
	if err := os.WriteFile(external, testmedia.PNG(), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, filepath.Join(root, "external.png")); err != nil {
		t.Fatal(err)
	}
	store := Store{Dir: filepath.Join(t.TempDir(), "attachments")}
	if files := store.ReplyCandidates(t.Context(), root); len(files) != 1 || files[0] != "photo.png" {
		t.Fatalf("candidates = %q", files)
	}
	if _, err := os.Stat(store.Dir); !os.IsNotExist(err) {
		t.Fatalf("discovery wrote to the store: %v", err)
	}
}

func TestOneLineCodeSpanDoesNotOpenAFence(t *testing.T) {
	root := t.TempDir()
	store := Store{Dir: t.TempDir()}
	p := writePictures(t, root, 2)
	chart, example := p[0], p[1]
	text, refs, err := store.ImportReply("```example```\nHere is the chart. ![Chart]("+chart+")", root)
	if err != nil || len(refs) != 1 || text != "```example```\nHere is the chart.\nChart" {
		t.Fatalf("after a one-line span: %q %d %v", text, len(refs), err)
	}
	for _, reply := range []string{
		"```![Example](<" + example + ">)```\nHere is the chart. ![Chart](<" + chart + ">)",
		"```\nExample: ![Example](<" + example + ">)\n```\nHere is the chart. ![Chart](<" + chart + ">)",
		"Wrap code in ``` marks.\nHere is the chart. ![Chart](<" + chart + ">)",
	} {
		for _, cleaned := range []string{reply, strings.ReplaceAll(ProtectImageExamples(reply), "```", "")} {
			_, refs, err := store.ImportReply(cleaned, root)
			if err != nil || len(refs) != 1 || refs[0].Name != filepath.Base(chart) {
				t.Errorf("%q: %+v %v", cleaned, refs, err)
			}
		}
	}
}

func TestEscapedReferenceStaysText(t *testing.T) {
	root := t.TempDir()
	store := Store{Dir: t.TempDir()}
	real := writePictures(t, root, 1)[0]
	for _, text := range []string{
		"For syntax, write \\![demo](missing.png).",
		"For syntax, write \\![demo](<" + real + ">).",
		"\\![demo](<" + real + ">)",
		"Both \\![a](missing.png) and \\\\\\![b](missing.png) are text.",
	} {
		got, refs, err := store.ImportReply(text, root)
		if got != text || len(refs) != 0 || err != nil || store.CheckReplyFiles(text, root) != nil {
			t.Errorf("%q: %q %d %v", text, got, len(refs), err)
		}
		if SeparateImageReferences(text) != text || ReplyProse(text) != text {
			t.Errorf("%q: lifted an escaped reference", text)
		}
	}
	// An escaped backslash leaves the reference live.
	got, refs, err := store.ImportReply("\\\\![Chart](<"+real+">)", root)
	if err != nil || len(refs) != 1 || got != "\\\\\nChart" {
		t.Fatalf("escaped backslash: %q %d %v", got, len(refs), err)
	}
}
