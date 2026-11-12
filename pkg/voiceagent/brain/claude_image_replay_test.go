package brain

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/lancekrogers/claude-code-go/pkg/claude"

	"github.com/Obedience-Corp/obey-voice/internal/testmedia"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/chatmedia"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

// replayEntries reads what a content call carried after its prompt: "sent:<id>"
// for an image and its label, "skipped:<id>" for a label saying an image is no
// longer attached.
func replayEntries(t *testing.T, blocks []claude.ContentBlock) []string {
	t.Helper()
	var out []string
	for i := 1; i < len(blocks); i++ {
		block := blocks[i]
		if block.Type != claude.ContentBlockText {
			t.Fatalf("block %d is an image without a label", i)
		}
		fields := strings.Fields(block.Text)
		if len(fields) < 3 {
			t.Fatalf("block %d: %q is not an image label", i, block.Text)
		}
		id := fields[2]
		switch {
		case strings.Contains(block.Text, "no longer attached"):
			out = append(out, "skipped:"+id)
		case i+1 < len(blocks) && blocks[i+1].Type == claude.ContentBlockImage:
			out = append(out, "sent:"+id)
			i++
		default:
			t.Fatalf("block %d: %q is neither an image label nor a skipped image", i, block.Text)
		}
	}
	return out
}

func entries(kind string, refs ...chatmedia.Ref) []string {
	var out []string
	for _, ref := range refs {
		out = append(out, kind+":"+ref.ID)
	}
	return out
}

// heldImageTurns is a conversation in which each ref was attached to its own
// user line and answered.
func heldImageTurns(refs ...chatmedia.Ref) []Turn {
	var turns []Turn
	for i, ref := range refs {
		turns = append(turns,
			Turn{Role: "user", Content: fmt.Sprintf("Look at image %d.", i+1), Attachments: []chatmedia.Ref{ref}},
			Turn{Role: "samantha", Content: "Seen."})
	}
	return turns
}

// freshRequest runs one request of the given kind in a fresh session after
// held, lines the provider answered itself, and returns the content calls it
// made.
func freshRequest(t *testing.T, kind string, held []Turn, refs ...chatmedia.Ref) [][]claude.ContentBlock {
	t.Helper()
	fake := &fakeClaudeClient{
		streamScripts: []streamScript{{msgs: []claude.Message{assistantMsg("Seen."), resultMsg("sess-1")}}},
		fullResults:   []*claude.ClaudeResult{{Result: "Seen.", SessionID: "sess-1"}},
	}
	b := newTestBrain(fake)
	b.history = slices.Clone(held)
	var err error
	switch kind {
	case "stream":
		var s *Stream
		if s, err = b.ThinkStream(context.Background(), "And now?", StreamOptions{Attachments: refs}); err == nil {
			_, res := collectStream(t, s)
			err = res.Err
		}
	case "blocking":
		_, err = b.ThinkFull(context.Background(), "And now?", StreamOptions{Attachments: refs})
	case "compaction":
		catchUp := &SessionCatchUp{Turns: held}
		_, err = b.ThinkFull(context.Background(), "Summarize the conversation.", StreamOptions{TransientHistory: true, OmitTurnInstruction: true, CatchUp: catchUp})
	}
	if err != nil {
		t.Fatalf("%s request failed: %v", kind, err)
	}
	return fake.content
}

// oneContentCall is the content call of a request that made exactly one.
func oneContentCall(t *testing.T, calls [][]claude.ContentBlock) []claude.ContentBlock {
	t.Helper()
	if len(calls) != 1 {
		t.Fatalf("the request made %d content calls", len(calls))
	}
	return calls[0]
}

func TestClaudeFreshSessionReplaysTheNewestEarlierImagesWithin32MiB(t *testing.T) {
	config.SetConfigDirForTest(t, t.TempDir())
	var earlier []chatmedia.Ref
	for i := range 6 {
		earlier = append(earlier, storedBlob(t, fmt.Sprintf("scan-%d.png", i+1), 7<<20))
	}
	held := heldImageTurns(earlier...)
	fresh := importImage(t, "new.png", testmedia.PNG())
	for _, kind := range []string{"stream", "blocking"} {
		t.Run(kind, func(t *testing.T) {
			refs := []chatmedia.Ref{fresh}
			got := replayEntries(t, oneContentCall(t, freshRequest(t, kind, held, refs...)))
			want := append(entries("skipped", earlier[:2]...), entries("sent", earlier[2:]...)...)
			want = append(want, entries("sent", refs...)...)
			if !slices.Equal(got, want) {
				t.Fatalf("replay = %q\nwant %q", got, want)
			}
		})
	}
}

func TestClaudeFreshSessionReplaysAtMostTwentyImages(t *testing.T) {
	config.SetConfigDirForTest(t, t.TempDir())
	var earlier []chatmedia.Ref
	for i := range 25 {
		earlier = append(earlier, storedBlob(t, fmt.Sprintf("note-%d.png", i+1), 64))
	}
	fresh := []chatmedia.Ref{storedBlob(t, "new-1.png", 64), storedBlob(t, "new-2.png", 64)}
	got := replayEntries(t, oneContentCall(t, freshRequest(t, "stream", heldImageTurns(earlier...), fresh...)))
	want := append(entries("skipped", earlier[:7]...), entries("sent", earlier[7:]...)...)
	want = append(want, entries("sent", fresh...)...)
	if !slices.Equal(got, want) {
		t.Fatalf("replay = %q\nwant %q", got, want)
	}
}

func TestClaudeReplaySkipsAnEarlierImageThatNoLongerLoads(t *testing.T) {
	config.SetConfigDirForTest(t, t.TempDir())
	kept := importImage(t, "kept.png", testmedia.PNG())
	gone := importImage(t, "gone.gif", testmedia.GIF())
	if err := os.Remove(chatmedia.DefaultStore().Path(gone)); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"stream", "blocking"} {
		t.Run(kind, func(t *testing.T) {
			blocks := oneContentCall(t, freshRequest(t, kind, heldImageTurns(kept, gone)))
			if got, want := replayEntries(t, blocks), []string{"sent:" + kept.ID, "skipped:" + gone.ID}; !slices.Equal(got, want) {
				t.Fatalf("replay = %q, want %q", got, want)
			}
			if label := blocks[len(blocks)-1].Text; !strings.Contains(label, "gone.gif") {
				t.Fatalf("the skipped image's label does not name it: %q", label)
			}
		})
	}
}

// A summary request on a fresh session is sent the conversation as text: the
// record names the earlier lines, and their images are not sent.
func TestClaudeSummaryRequestReplaysNoImages(t *testing.T) {
	config.SetConfigDirForTest(t, t.TempDir())
	kept := importImage(t, "kept.png", testmedia.PNG())
	if calls := freshRequest(t, "compaction", heldImageTurns(kept)); len(calls) != 0 {
		t.Fatalf("a summary request sent images: %d content calls", len(calls))
	}
}

// Lines loaded into the provider, whoever recorded them, never send their
// images: only a line the provider answered itself keeps them.
func TestClaudeNeverSendsTheImagesOfLoadedLines(t *testing.T) {
	config.SetConfigDirForTest(t, t.TempDir())
	saved := importImage(t, "saved.png", testmedia.PNG())
	other := importImage(t, "ember.png", testmedia.PNG())
	turns := []Turn{
		{Role: "user", Content: "Look at this.", Attachments: []chatmedia.Ref{saved}},
		{Role: "assistant", Content: "Seen.", PersonaID: "mira"},
		{Role: "user", Content: "Ember, and this?", Attachments: []chatmedia.Ref{other}},
		{Role: "assistant", Content: "Noted.", PersonaID: "ember"},
	}
	for _, load := range []string{"load", "reseed"} {
		for _, kind := range []string{"stream", "blocking"} {
			t.Run(load+"/"+kind, func(t *testing.T) {
				fake := &fakeClaudeClient{
					streamScripts: []streamScript{{msgs: []claude.Message{assistantMsg("Seen."), resultMsg("sess-1")}}},
					fullResults:   []*claude.ClaudeResult{{Result: "Seen.", SessionID: "sess-1"}},
				}
				b := newTestBrain(fake)
				if load == "load" {
					b.LoadHistory(turns)
				} else {
					b.ReseedContinuity(turns)
				}
				if kind == "stream" {
					s, err := b.ThinkStream(context.Background(), "Remember them?", StreamOptions{})
					if err != nil {
						t.Fatal(err)
					}
					collectStream(t, s)
				} else if _, err := b.ThinkFull(context.Background(), "Remember them?", StreamOptions{}); err != nil {
					t.Fatal(err)
				}
				if len(fake.content) != 0 {
					t.Fatalf("a loaded line's image was sent: %d content calls", len(fake.content))
				}
				if turns[0].Attachments == nil {
					t.Fatal("loading changed the caller's turns")
				}
			})
		}
	}
}

// TestClaudeImageTurnsTreatAnEmptyReplyAsTextTurnsDo runs the SDK against a
// fake claude that answers with no text, or with only a filler. A turn with an
// image ends as the same turn without one does, streamed or blocking: the SDK
// reports an empty result as an error on every path, and a filler-only reply
// is replaced by the fallback line.
func TestClaudeImageTurnsTreatAnEmptyReplyAsTextTurnsDo(t *testing.T) {
	config.SetConfigDirForTest(t, t.TempDir())
	ref := importImage(t, "diagram.png", testmedia.PNG())
	bin := filepath.Join(t.TempDir(), "claude")
	script := `#!/bin/sh
cat > /dev/null
text=''
[ "$OBEY_TEST_CLAUDE_REPLY" = filler ] && text='Hmm.'
case "$*" in
*'--output-format json'*) printf '{"type":"result","subtype":"success","result":"%s","session_id":"s1"}\n' "$text" ;;
*) printf '%s\n' '{"type":"system","subtype":"init","session_id":"s1"}'
   [ -n "$text" ] && printf '{"type":"assistant","session_id":"s1","message":{"content":[{"type":"text","text":"%s"}]}}\n' "$text"
   printf '{"type":"result","subtype":"success","result":"%s","session_id":"s1"}\n' "$text" ;;
esac
`
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	type outcome struct {
		reply  string
		failed bool
	}
	run := func(blocking bool, refs []chatmedia.Ref) outcome {
		t.Helper()
		b := newTestBrain(claude.NewClient(bin))
		opts := StreamOptions{Attachments: refs}
		if blocking {
			reply, err := b.ThinkFull(context.Background(), "What is this?", opts)
			return outcome{reply, err != nil}
		}
		s, err := b.ThinkStream(context.Background(), "What is this?", opts)
		if err != nil {
			t.Fatal(err)
		}
		if _, res := collectStream(t, s); res.Err != nil {
			return outcome{failed: true}
		}
		history := b.History()
		return outcome{reply: history[len(history)-1].Content}
	}
	for _, reply := range []string{"empty", "filler"} {
		t.Setenv("OBEY_TEST_CLAUDE_REPLY", reply)
		for _, blocking := range []bool{false, true} {
			text, image := run(blocking, nil), run(blocking, []chatmedia.Ref{ref})
			if image != text {
				t.Fatalf("%s reply, blocking=%v: image turn = %+v, text turn = %+v", reply, blocking, image, text)
			}
			if want := (outcome{reply: fallbackResponse}); reply == "filler" && image != want {
				t.Fatalf("filler reply, blocking=%v: %+v, want the fallback line", blocking, image)
			}
			if reply == "empty" && !image.failed {
				t.Fatalf("empty reply, blocking=%v: %+v, want the error a text turn gets", blocking, image)
			}
		}
	}
}
