package brain

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/chatmedia"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

// A model shown a picture the user pasted often calls view_image with the
// picture's label or name joined to its work directory. That file never
// exists there, so view_image finds the picture on the conversation instead.
func TestViewImageFindsAPictureTheUserAttached(t *testing.T) {
	o, ref := sharedPicture(t)
	o.workDir = t.TempDir()
	if err := o.appendUserInput("What is in it?", StreamOptions{Attachments: []chatmedia.Ref{ref}}); err != nil {
		t.Fatal(err)
	}
	for name, path := range map[string]string{
		"label in the work directory": filepath.Join(o.workDir, "Attached image. ("+ref.Name+")"),
		"name in another case":        strings.ToUpper(ref.Name),
		"id":                          ref.ID,
	} {
		t.Run(name, func(t *testing.T) {
			result, _, end := o.viewImage(t.Context(), path)
			if end.status != events.ToolOutcomeSucceeded || !strings.Contains(result, "already attached") {
				t.Fatalf("result %q, status %s", result, end.status)
			}
		})
	}
}

// A picture the user attached to an earlier message can be brought back by
// its id.
func TestViewImageReopensAnEarlierAttachedPictureByID(t *testing.T) {
	o, ref := sharedPicture(t)
	o.workDir = t.TempDir()
	if err := o.appendUserInput("And the one before?", StreamOptions{}); err != nil {
		t.Fatal(err)
	}
	result, _, end := o.viewImage(t.Context(), ref.ID)
	if end.status != events.ToolOutcomeSucceeded || strings.HasPrefix(result, "error:") {
		t.Fatalf("result %q, status %s", result, end.status)
	}
}

// A path naming no attached picture still fails, and the failure names the
// pictures the user attached, so it does not read as being unable to see.
func TestViewImageMissNamesTheAttachedPictures(t *testing.T) {
	o, ref := sharedPicture(t)
	o.workDir = t.TempDir()
	if err := o.appendUserInput("What is in it?", StreamOptions{Attachments: []chatmedia.Ref{ref}}); err != nil {
		t.Fatal(err)
	}
	// "mydesk.png" contains "desk.png" but is a different name.
	for _, path := range []string{"other.png", "my" + ref.Name} {
		result, _, end := o.viewImage(t.Context(), path)
		if end.status == events.ToolOutcomeSucceeded || !strings.Contains(result, pictureLabel(ref)) || !strings.Contains(result, "not in your work directory") {
			t.Fatalf("%s: result %q, status %s", path, result, end.status)
		}
	}
}
