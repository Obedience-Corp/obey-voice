package work

import (
	"encoding/json"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/chatmedia"
)

func TestImageOutcomesAreImmutableAndRestore(t *testing.T) {
	scope := testScope()
	c := NewCoordinator(scope)
	key, _ := c.Register(scope, Admission{Task: "Draw"})
	refs := []chatmedia.Ref{{ID: "image", Name: "result.gif", Frames: 6}}
	o := Outcome{Scope: scope, JobID: key.JobID, Status: OutcomeSucceeded, Text: "Result", Attachments: refs}
	first, err := c.Publish(key, o)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.Publish(key, o); err != nil {
		t.Fatal(err)
	}
	refs[0].Name = "caller mutation"
	first.Outcome.Attachments[0].Name = "return mutation"
	snap := c.Snapshot(scope)
	if snap.Pending[0].Outcome.Attachments[0].Name != "result.gif" {
		t.Fatal("publish retained alias")
	}
	bytes, err := json.Marshal(snap)
	if err != nil {
		t.Fatal(err)
	}
	var disk Snapshot
	if err = json.Unmarshal(bytes, &disk); err != nil {
		t.Fatal(err)
	}
	next := WorkScope{scope.SessionID, "new-runtime", "new-conversation"}
	restored := NewCoordinator(scope)
	if err = restored.Restore(disk, next); err != nil {
		t.Fatal(err)
	}
	disk.Pending[0].Outcome.Attachments[0].Name = "disk mutation"
	got := restored.Pending(next)
	if len(got) != 1 || got[0].Outcome.Attachments[0].Name != "result.gif" || !got[0].Outcome.PreviousSession {
		t.Fatalf("restored %+v", got)
	}
	disk = snap
	disk.Pending[0].Outcome.Attachments[0].ID = "wrong-image"
	if err = NewCoordinator(scope).Restore(disk, next); err == nil {
		t.Fatal("accepted inconsistent persisted result")
	}
}
