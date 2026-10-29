package brain

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/Obedience-Corp/obey-voice/internal/testmedia"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/chatmedia"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

func TestCodexReceivesNativeImagesAndRehydratesHistory(t *testing.T) {
	config.SetConfigDirForTest(t, t.TempDir())
	ref, err := chatmedia.DefaultStore().Import("input.gif", testmedia.GIF())
	if err != nil {
		t.Fatal(err)
	}
	c, f := codexFixture(t)
	if _, err = c.ThinkFull(t.Context(), "Inspect", StreamOptions{Attachments: []chatmedia.Ref{ref}}); err != nil {
		t.Fatal(err)
	}
	check := func(index int) {
		t.Helper()
		images := f.requests[index].Images
		if len(images) != 2 {
			t.Fatalf("images=%+v", images)
		}
		image := images[1].(map[string]any)
		encoded := strings.TrimPrefix(image["url"].(string), "data:image/png;base64,")
		data, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil || image["type"] != "image" || !bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")) {
			t.Fatal("native GIF first-frame transport missing")
		}
	}
	check(0)
	if c.History()[0].Attachments[0] != ref {
		t.Fatal("input not persisted")
	}
	if _, err = c.ThinkFull(t.Context(), "Next", StreamOptions{}); err != nil {
		t.Fatal(err)
	}
	if len(f.requests[1].Images) != 0 {
		t.Fatal("resumed native session duplicated images")
	}
	c.LoadHistory(c.History())
	if _, err = c.ThinkFull(t.Context(), "Reloaded", StreamOptions{}); err != nil {
		t.Fatal(err)
	}
	check(2)
	before := len(c.History())
	_ = os.Remove(chatmedia.DefaultStore().Path(ref))
	if _, err = c.ThinkFull(t.Context(), "Missing", StreamOptions{Attachments: []chatmedia.Ref{ref}}); err == nil {
		t.Fatal("missing media silently dropped")
	}
	if len(c.History()) != before || len(f.requests) != 3 {
		t.Fatal("invalid input dispatched or mutated history")
	}
}
func TestReplyMediaPreservesIdentityAndNativeSession(t *testing.T) {
	c, _ := codexFixture(t)
	_, err := c.ThinkFull(t.Context(), "Create", StreamOptions{Identity: &TurnIdentity{ResponseID: "reply-id", PersonaID: "artist"}})
	if err != nil {
		t.Fatal(err)
	}
	id := c.sessionID
	refs := []chatmedia.Ref{{ID: "fixture", Name: "output.png"}}
	if err = c.SetReplyMedia("Result", refs); err != nil {
		t.Fatal(err)
	}
	refs[0].Name = "changed"
	for _, h := range [][]Turn{c.History(), c.ContextHistory()} {
		reply := h[len(h)-1]
		if reply.Content != "Result" || reply.ResponseID != "reply-id" || reply.PersonaID != "artist" || reply.Attachments[0].Name != "output.png" {
			t.Fatalf("%+v", reply)
		}
	}
	if c.sessionID != id {
		t.Fatal("media reset native session")
	}
}

func TestCodexImageInputReachesAppServerWire(t *testing.T) {
	config.SetConfigDirForTest(t, t.TempDir())
	ref, err := chatmedia.DefaultStore().Import("input.png", testmedia.PNG())
	if err != nil {
		t.Fatal(err)
	}
	c, _ := codexFixture(t)
	client, log := codexProcessFixture(t, "")
	c.client = client
	if _, err = c.ThinkFull(t.Context(), "Inspect", StreamOptions{Attachments: []chatmedia.Ref{ref}}); err != nil {
		t.Fatal(err)
	}
	wire, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(wire), "\n") {
		var message struct {
			Method string
			Params struct{ Input []struct{ Type, URL string } }
		}
		_ = json.Unmarshal([]byte(line), &message)
		if message.Method != "turn/start" {
			continue
		}
		for _, input := range message.Params.Input {
			if input.Type != "image" {
				continue
			}
			data, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(input.URL, "data:image/png;base64,"))
			if err != nil || !bytes.Equal(data, testmedia.PNG()) {
				t.Fatal("wire payload did not preserve bytes")
			}
			return
		}
	}
	t.Fatal("native image was absent from turn/start")
}

func TestImageReplyIsACompleteResponse(t *testing.T) {
	for _, tc := range []struct {
		text       string
		incomplete bool
	}{
		{"Here is the image.\n![Result](</tmp/result.png>)", false},
		{"![Result](</tmp/result.png>)", false},
		{"I was about to\n![Result](</tmp/result.png>)", true},
		{"![Result](</tmp/res", true},
	} {
		got := evaluateStreamIntegrity(IntegrityInput{DoneReason: "stop", Speakable: tc.text, GenTokens: 20})
		if got.Truncated != tc.incomplete {
			t.Fatalf("%q: %+v", tc.text, got)
		}
	}
	if !evaluateStreamIntegrity(IntegrityInput{DoneReason: "length", Speakable: "![Result](</tmp/result.png>)"}).Truncated {
		t.Fatal("image hid output budget truncation")
	}
}

func TestReplyMediaWithDiscardedWorkingReply(t *testing.T) {
	canonical := []Turn{{Role: "assistant", Content: "new reply", ResponseID: "new"}}
	working := []Turn{{Role: "assistant", Content: "old summary", ResponseID: "old"}}
	refs := []chatmedia.Ref{{ID: "asset"}}
	if err := setMediaViews(canonical, working, "caption", refs); err != nil {
		t.Fatal(err)
	}
	if len(canonical[0].Attachments) != 1 || len(working[0].Attachments) != 0 || working[0].Content != "old summary" {
		t.Fatal("image was lost or attached to old summary")
	}
	if err := setMediaViews(canonical, nil, "caption", refs); err != nil {
		t.Fatal("trimmed view lost canonical reply:", err)
	}
}
