package personaruntime

import (
	"bytes"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeChart(t *testing.T, dir string) {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewGray(image.Rect(0, 0, 24, 16))); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "chart.png"), buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
}

// A picture a persona posts reaches the model on the next user message, and
// another persona sees it named for the one who posted it.
func TestPostedPictureReachesTheNextRequestAndTheNextPersona(t *testing.T) {
	c := newLocalConversationWithTools(t, 20, "", "ember", "scout")
	writeChart(t, c.root)
	c.say(drawTheChart, "What colour is it?")
	if sent := c.lastOllamaRequest(); !strings.Contains(sent, `"images":[`) || !strings.Contains(sent, "(chart.png): a picture you posted.") {
		t.Fatalf("the next request after the posted picture:\n%s", sent)
	}
	c.say("switch to Scout", "What do you make of it?")
	if sent := c.lastOllamaRequest(); !strings.Contains(sent, `"images":[`) || !strings.Contains(sent, "(chart.png): a picture Ember posted.") {
		t.Fatalf("Scout was sent:\n%s", sent)
	}
	for _, turn := range c.saved().Turns {
		if turn.Role == "user" && (len(turn.Attachments) > 0 || strings.Contains(turn.Content, "Attached image")) {
			t.Fatalf("a user line holds the posted picture: %+v", turn)
		}
	}
	if errs := c.errorEvents(); len(errs) > 0 {
		t.Fatalf("errors: %+v", errs)
	}
}
