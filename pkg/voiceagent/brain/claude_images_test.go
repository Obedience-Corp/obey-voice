package brain

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/lancekrogers/claude-code-go/pkg/claude"

	"github.com/Obedience-Corp/obey-voice/internal/testmedia"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/chatmedia"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

func importImage(t *testing.T, name string, data []byte) chatmedia.Ref {
	t.Helper()
	ref, err := chatmedia.DefaultStore().Import(name, data)
	if err != nil {
		t.Fatal(err)
	}
	return ref
}

func jpegFixture(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	img.Set(1, 1, color.RGBA{R: 200, A: 255})
	var out bytes.Buffer
	if err := jpeg.Encode(&out, img, nil); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

// storedBlob puts size random bytes in the attachment store under their own
// id, as a stored PNG the store reads back without decoding.
func storedBlob(t *testing.T, name string, size int) chatmedia.Ref {
	t.Helper()
	data := make([]byte, size)
	if _, err := rand.Read(data); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	ref := chatmedia.Ref{ID: hex.EncodeToString(sum[:]), Name: name, Format: "png"}
	path := chatmedia.DefaultStore().Path(ref)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return ref
}

// sentImage is one image a content call carried after its label.
type sentImage struct {
	label, mediaType string
	data             []byte
}

// splitContent checks a content call's shape, a prompt then label and image
// pairs, and returns the prompt and the images.
func splitContent(t *testing.T, blocks []claude.ContentBlock) (string, []sentImage) {
	t.Helper()
	if len(blocks) < 3 || len(blocks)%2 != 1 || blocks[0].Type != claude.ContentBlockText {
		t.Fatalf("content blocks = %+v, want the prompt then label and image pairs", blocks)
	}
	var images []sentImage
	for i := 1; i < len(blocks); i += 2 {
		label, img := blocks[i], blocks[i+1]
		if label.Type != claude.ContentBlockText || img.Type != claude.ContentBlockImage || img.Source == nil {
			t.Fatalf("block %d: %+v %+v, want a label then an image", i, label, img)
		}
		data, err := base64.StdEncoding.DecodeString(img.Source.Data)
		if err != nil {
			t.Fatal(err)
		}
		images = append(images, sentImage{label: label.Text, mediaType: img.Source.MediaType, data: data})
	}
	return blocks[0].Text, images
}

func imageLabel(ref chatmedia.Ref) string {
	return "Attached image " + ref.ID + " (" + ref.Name + "):"
}

func requireImages(t *testing.T, got []sentImage, want ...chatmedia.Ref) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("sent %d images, want %d", len(got), len(want))
	}
	for i, ref := range want {
		if got[i].label != imageLabel(ref) {
			t.Fatalf("image %d label = %q, want %q", i, got[i].label, imageLabel(ref))
		}
	}
}

func TestClaudeImageTurnSendsThePromptThenEachLabelledImage(t *testing.T) {
	config.SetConfigDirForTest(t, t.TempDir())
	png := importImage(t, "diagram.png", testmedia.PNG())
	gif := importImage(t, "loop.gif", testmedia.GIF())
	photo := importImage(t, "photo.jpg", jpegFixture(t))
	refs := []chatmedia.Ref{png, gif, photo}

	// ask runs one turn on a new Brain and returns the prompt it sent.
	ask := func(fake *fakeClaudeClient, blocking bool, refs []chatmedia.Ref) (*Brain, string) {
		t.Helper()
		fake.streamScripts = []streamScript{{msgs: []claude.Message{assistantMsg("Seen."), resultMsg("sess-1")}}}
		fake.fullResults = []*claude.ClaudeResult{{Result: "Seen.", SessionID: "sess-1"}}
		b := newTestBrain(fake)
		opts := StreamOptions{Attachments: refs}
		if blocking {
			if _, err := b.ThinkFull(context.Background(), "What is this?", opts); err != nil {
				t.Fatal(err)
			}
			if fake.runCalls != 1 || len(fake.stdinPrompts) != 0 || fake.runOpts[0].Format != claude.JSONOutput {
				t.Fatalf("blocking calls = %d, piped = %d", fake.runCalls, len(fake.stdinPrompts))
			}
			return b, fake.runPrompts[0]
		}
		s, err := b.ThinkStream(context.Background(), "What is this?", opts)
		if err != nil {
			t.Fatal(err)
		}
		if _, res := collectStream(t, s); res.Err != nil {
			t.Fatal(res.Err)
		}
		if fake.streamCalls != 1 || fake.streamOpts[0].Format != claude.StreamJSONOutput {
			t.Fatalf("stream calls = %d", fake.streamCalls)
		}
		return b, fake.streamPrompts[0]
	}

	for _, blocking := range []bool{false, true} {
		_, textPrompt := ask(&fakeClaudeClient{}, blocking, nil)
		fake := &fakeClaudeClient{}
		b, _ := ask(fake, blocking, refs)
		if len(fake.content) != 1 {
			t.Fatalf("content calls = %d, want 1", len(fake.content))
		}
		prompt, images := splitContent(t, fake.content[0])
		if prompt != textPrompt {
			t.Fatalf("the image turn's prompt differs from the text path's:\n%q\n%q", prompt, textPrompt)
		}
		requireImages(t, images, refs...)
		if images[0].mediaType != "image/png" || !bytes.Equal(images[0].data, testmedia.PNG()) {
			t.Fatalf("PNG sent as %s, %d bytes", images[0].mediaType, len(images[0].data))
		}
		if images[1].mediaType != "image/png" || !bytes.HasPrefix(images[1].data, []byte("\x89PNG\r\n\x1a\n")) {
			t.Fatalf("GIF sent as %s, want its first frame as PNG", images[1].mediaType)
		}
		if images[2].mediaType != "image/jpeg" || !bytes.Equal(images[2].data, jpegFixture(t)) {
			t.Fatalf("JPEG sent as %s", images[2].mediaType)
		}
		if got := b.History()[0].Attachments; len(got) != 3 || got[0] != png {
			t.Fatalf("the user turn did not keep its attachments: %+v", got)
		}
	}
}

func TestClaudeTextOnlyTurnsNeverUseTheContentPath(t *testing.T) {
	config.SetConfigDirForTest(t, t.TempDir())
	ref := importImage(t, "diagram.png", testmedia.PNG())
	fake := &fakeClaudeClient{
		streamScripts: []streamScript{
			{msgs: []claude.Message{assistantMsg("Hi."), resultMsg("sess-1")}},
			{msgs: []claude.Message{assistantMsg("Seen."), resultMsg("sess-1")}},
			{msgs: []claude.Message{assistantMsg("Sure."), resultMsg("sess-1")}},
		},
		fullResults: []*claude.ClaudeResult{{Result: "Done.", SessionID: "sess-1"}},
	}
	b := newTestBrain(fake)
	run := func(input string, refs ...chatmedia.Ref) {
		t.Helper()
		s, err := b.ThinkStream(context.Background(), input, StreamOptions{Attachments: refs})
		if err != nil {
			t.Fatal(err)
		}
		if _, res := collectStream(t, s); res.Err != nil {
			t.Fatal(res.Err)
		}
	}
	run("Hello.")
	if len(fake.content) != 0 || b.History()[0].Attachments != nil {
		t.Fatalf("a text-only turn used the content path or recorded attachments: %d", len(fake.content))
	}
	run("Look at this.", ref)
	// The resumed session already holds the image.
	run("And now?")
	if _, err := b.ThinkFull(context.Background(), "Summarize.", StreamOptions{}); err != nil {
		t.Fatal(err)
	}
	if len(fake.content) != 1 || fake.streamCalls != 3 || fake.runCalls != 1 {
		t.Fatalf("content calls = %d, want only the image turn's", len(fake.content))
	}
	if fake.streamOpts[2].ResumeID != "sess-1" || fake.runOpts[0].ResumeID != "sess-1" {
		t.Fatal("text-only turns did not resume the session")
	}
}

func TestClaudeResumedSessionSendsOnlyTheNewTurnsImages(t *testing.T) {
	config.SetConfigDirForTest(t, t.TempDir())
	first := importImage(t, "first.png", testmedia.PNG())
	second := importImage(t, "second.gif", testmedia.GIF())
	fake := &fakeClaudeClient{fullResults: []*claude.ClaudeResult{{Result: "One.", SessionID: "sess-1"}, {Result: "Two.", SessionID: "sess-1"}}}
	b := newTestBrain(fake)
	for _, ref := range []chatmedia.Ref{first, second} {
		if _, err := b.ThinkFull(context.Background(), "Look.", StreamOptions{Attachments: []chatmedia.Ref{ref}}); err != nil {
			t.Fatal(err)
		}
	}
	if len(fake.content) != 2 || fake.runOpts[1].ResumeID != "sess-1" {
		t.Fatalf("content calls = %d, resume = %q", len(fake.content), fake.runOpts[1].ResumeID)
	}
	_, images := splitContent(t, fake.content[1])
	requireImages(t, images, second)
}

func TestClaudeFreshSessionReplaysThePersonasOwnImages(t *testing.T) {
	config.SetConfigDirForTest(t, t.TempDir())
	own := importImage(t, "own.png", testmedia.PNG())
	again := importImage(t, "again.gif", testmedia.GIF())
	fake := &fakeClaudeClient{
		streamScripts: []streamScript{
			{msgs: []claude.Message{assistantMsg("Seen."), resultMsg("sess-1")}},
			{msgs: []claude.Message{assistantMsg("Still seen."), resultMsg("sess-2")}},
			// A rejected --resume, then the retry in a fresh session.
			{err: errors.New("no conversation found with session ID sess-2")},
			{msgs: []claude.Message{assistantMsg("Both."), resultMsg("sess-3")}},
		},
	}
	b := newTestBrain(fake)
	run := func(input string, refs ...chatmedia.Ref) {
		t.Helper()
		s, err := b.ThinkStream(context.Background(), input, StreamOptions{Attachments: refs})
		if err != nil {
			t.Fatal(err)
		}
		if _, res := collectStream(t, s); res.Err != nil {
			t.Fatal(res.Err)
		}
	}
	run("Look at this.", own)
	// A restored conversation has no session to resume.
	b.LoadHistory(b.History())
	run("Remember it?")
	if len(fake.content) != 2 || fake.streamOpts[1].ResumeID != "" {
		t.Fatalf("content calls = %d, resume = %q", len(fake.content), fake.streamOpts[1].ResumeID)
	}
	_, images := splitContent(t, fake.content[1])
	requireImages(t, images, own)

	run("And this one?", again, own)
	if len(fake.content) != 4 || fake.streamOpts[2].ResumeID != "sess-2" || fake.streamOpts[3].ResumeID != "" {
		t.Fatalf("content calls = %d", len(fake.content))
	}
	_, resumed := splitContent(t, fake.content[2])
	requireImages(t, resumed, again, own)
	_, retried := splitContent(t, fake.content[3])
	requireImages(t, retried, own, again)
}

func TestClaudeNeverSendsImagesFromCatchUpLines(t *testing.T) {
	config.SetConfigDirForTest(t, t.TempDir())
	other := importImage(t, "ember-only.png", testmedia.PNG())
	fake := &fakeClaudeClient{streamScripts: []streamScript{{msgs: []claude.Message{assistantMsg("Hi."), resultMsg("sess-1")}}}}
	b := newTestBrain(fake)
	catchUp := &SessionCatchUp{Turns: []Turn{
		{Role: "user", Content: "Ember, read this.", Attachments: []chatmedia.Ref{other}},
		{Role: "assistant", Content: "It says hi.", PersonaID: "ember", PersonaDisplayName: "Ember"},
	}}
	s, err := b.ThinkStream(context.Background(), "What did Ember read?", StreamOptions{CatchUp: catchUp})
	if err != nil {
		t.Fatal(err)
	}
	collectStream(t, s)
	if len(fake.content) != 0 || fake.streamCalls != 1 {
		t.Fatalf("a catch-up line's image was sent: %d content calls", len(fake.content))
	}
}

func TestClaudeRejectsBadImagesBeforeAnyCall(t *testing.T) {
	config.SetConfigDirForTest(t, t.TempDir())
	ok := importImage(t, "ok.png", testmedia.PNG())
	missing := importImage(t, "gone.gif", testmedia.GIF())
	if err := os.Remove(chatmedia.DefaultStore().Path(missing)); err != nil {
		t.Fatal(err)
	}
	oversize := storedBlob(t, "huge.png", chatmedia.MaxBytes+1)
	five := []chatmedia.Ref{ok, ok, ok, ok, ok}
	var large []chatmedia.Ref
	for i := range 5 {
		large = append(large, storedBlob(t, "large"+string(rune('a'+i))+".png", 7<<20))
	}

	cases := map[string]struct {
		held  []Turn
		input []chatmedia.Ref
		want  string
	}{
		"five images":       {input: five, want: "at most four images"},
		"unreadable":        {input: []chatmedia.Ref{ok, missing}, want: "gone.gif"},
		"over 8 MiB":        {input: []chatmedia.Ref{oversize}, want: "attachment content has changed"},
		"replay too big":    {held: []Turn{{Role: "user", Content: "Look.", Attachments: large[:3]}, {Role: "samantha", Content: "Seen."}}, input: large[3:], want: "exceed 32 MiB"},
		"replay unreadable": {held: []Turn{{Role: "user", Content: "Look.", Attachments: []chatmedia.Ref{missing}}, {Role: "samantha", Content: "Seen."}}, want: "gone.gif"},
	}
	for name, tc := range cases {
		for _, blocking := range []bool{false, true} {
			t.Run(name+map[bool]string{false: "/stream", true: "/blocking"}[blocking], func(t *testing.T) {
				fake := &fakeClaudeClient{}
				b := newTestBrain(fake)
				b.LoadHistory(tc.held)
				var err error
				if blocking {
					_, err = b.ThinkFull(context.Background(), "Look.", StreamOptions{Attachments: tc.input})
				} else {
					_, err = b.ThinkStream(context.Background(), "Look.", StreamOptions{Attachments: tc.input})
				}
				if err == nil || !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("err = %v, want %q", err, tc.want)
				}
				if fake.streamCalls+fake.runCalls != 0 || len(b.History()) != len(tc.held) {
					t.Fatalf("a rejected image turn reached the CLI or history: %d calls, %d turns", fake.streamCalls+fake.runCalls, len(b.History()))
				}
			})
		}
	}

	b := newTestBrain(&fakeClaudeClient{})
	if err := b.ValidateImages(context.Background(), []chatmedia.Ref{ok}); err != nil {
		t.Fatal(err)
	}
	for _, refs := range [][]chatmedia.Ref{five, {missing}, {oversize}} {
		if err := b.ValidateImages(context.Background(), refs); err == nil {
			t.Fatalf("ValidateImages accepted %+v", refs)
		}
	}
}

func TestClaudeForgetsTheImagesOfAFailedTurn(t *testing.T) {
	config.SetConfigDirForTest(t, t.TempDir())
	ref := importImage(t, "rejected.png", testmedia.PNG())
	fake := &fakeClaudeClient{streamScripts: []streamScript{
		{err: errors.New("image rejected")},
		{msgs: []claude.Message{assistantMsg("Hi."), resultMsg("sess-1")}},
	}}
	b := newTestBrain(fake)
	s, err := b.ThinkStream(context.Background(), "Look.", StreamOptions{Attachments: []chatmedia.Ref{ref}})
	if err != nil {
		t.Fatal(err)
	}
	if _, res := collectStream(t, s); res.Err == nil {
		t.Fatal("the failed turn reported success")
	}
	s, err = b.ThinkStream(context.Background(), "Hello?", StreamOptions{})
	if err != nil {
		t.Fatal(err)
	}
	collectStream(t, s)
	if len(fake.content) != 1 || b.History()[0].Attachments != nil {
		t.Fatalf("a failed turn's image is replayed: %d content calls", len(fake.content))
	}
}

// TestClaudeImageRequestKeepsALargePromptOffTheCommandLine runs the SDK
// against a fake claude executable that records its arguments and stdin.
func TestClaudeImageRequestKeepsALargePromptOffTheCommandLine(t *testing.T) {
	config.SetConfigDirForTest(t, t.TempDir())
	ref := importImage(t, "diagram.png", testmedia.PNG())
	dir := t.TempDir()
	args, stdin := filepath.Join(dir, "args"), filepath.Join(dir, "stdin")
	script := "#!/bin/sh\n" +
		"for a in \"$@\"; do printf '%s\\n' \"${#a}\" >> '" + args + "'; done\n" +
		"cat > '" + stdin + "'\n" +
		`printf '%s\n' '{"type":"system","subtype":"init","session_id":"s1"}' '{"type":"assistant","session_id":"s1","message":{"content":[{"type":"text","text":"Seen."}]}}' '{"type":"result","subtype":"success","result":"Seen.","session_id":"s1"}'` + "\n"
	bin := filepath.Join(dir, "claude")
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, blocking := range []bool{false, true} {
		_ = os.Remove(args)
		b := newTestBrain(claude.NewClient(bin))
		request := "Describe this. " + strings.Repeat("teal ", 30000)
		opts := StreamOptions{Attachments: []chatmedia.Ref{ref}}
		if blocking {
			if _, err := b.ThinkFull(context.Background(), request, opts); err != nil {
				t.Fatal(err)
			}
		} else {
			s, err := b.ThinkStream(context.Background(), request, opts)
			if err != nil {
				t.Fatal(err)
			}
			if _, res := collectStream(t, s); res.Err != nil {
				t.Fatal(res.Err)
			}
		}
		lengths, err := os.ReadFile(args)
		if err != nil {
			t.Fatal(err)
		}
		for _, field := range strings.Fields(string(lengths)) {
			if n, err := strconv.Atoi(field); err != nil || n >= maxInlinePromptBytes {
				t.Fatalf("an argument of %s bytes reached the command line", field)
			}
		}
		sent, err := os.ReadFile(stdin)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(sent), "Describe this. teal") || !strings.Contains(string(sent), base64.StdEncoding.EncodeToString(testmedia.PNG())) {
			t.Fatal("the prompt and image did not reach stdin")
		}
	}
}
