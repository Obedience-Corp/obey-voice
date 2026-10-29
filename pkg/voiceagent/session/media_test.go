package session

import (
	"github.com/Obedience-Corp/obey-voice/internal/testmedia"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/chatmedia"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"testing"
)

func TestSessionRestoresAttachmentReferencesAndAssets(t *testing.T) {
	config.SetConfigDirForTest(t, t.TempDir())
	asset, err := chatmedia.DefaultStore().Import("picture.png", testmedia.PNG())
	if err != nil {
		t.Fatal(err)
	}
	s := New("ollama", "vision")
	if err = DefaultStore().Save(s, []brain.Turn{{Role: "user", Content: "Look", Attachments: []chatmedia.Ref{asset}}}); err != nil {
		t.Fatal(err)
	}
	restored, err := Load(s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(restored.Turns) != 1 || len(restored.Turns[0].Attachments) != 1 || restored.Turns[0].Attachments[0] != asset {
		t.Fatal("save/reopen lost attachment")
	}
	if _, err = chatmedia.DefaultStore().Preview(restored.Turns[0].Attachments[0]); err != nil {
		t.Fatal(err)
	}
}
