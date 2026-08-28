package pipeline

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

func TestSplitVocalizations(t *testing.T) {
	tests := []struct {
		in   string
		want []vocalPart
	}{
		{"Hello there.", []vocalPart{{text: "Hello there."}}},
		{"[moan]", []vocalPart{{sound: vocalMoan}}},
		{"[moan] that's it, baby.", []vocalPart{{sound: vocalMoan}, {text: "that's it, baby."}}},
		{"Come here. [gasp] Don't stop.", []vocalPart{{text: "Come here."}, {sound: vocalGasp}, {text: "Don't stop."}}},
		{"[laugh] [moan] more", []vocalPart{{sound: vocalLaugh}, {sound: vocalMoan}, {text: "more"}}},
		{"[AHH] yes", []vocalPart{{sound: vocalMoan}, {text: "yes"}}},
		{"", nil},
	}
	for _, tt := range tests {
		got := splitVocalizations(tt.in)
		if len(got) != len(tt.want) {
			t.Errorf("splitVocalizations(%q) = %#v, want %#v", tt.in, got, tt.want)
			continue
		}
		for i := range tt.want {
			if got[i] != tt.want[i] {
				t.Errorf("splitVocalizations(%q)[%d] = %#v, want %#v", tt.in, i, got[i], tt.want[i])
			}
		}
	}
}

func TestStripVocalTags(t *testing.T) {
	if got := stripVocalTags("[moan] that's it, baby."); got != "that's it, baby." {
		t.Errorf("stripVocalTags = %q", got)
	}
	if got := stripVocalTags("[moan]"); got != "" {
		t.Errorf("tag-only strip = %q, want empty", got)
	}
	if got := stripVocalTags("plain speech"); got != "plain speech" {
		t.Errorf("untagged = %q", got)
	}
}

func TestExpandVocalTagsUsesHumanOnomatopoeia(t *testing.T) {
	got := expandVocalTags("[moan] that's it, baby.", nil)
	if strings.Contains(strings.ToLower(got), "[moan]") {
		t.Errorf("markup leaked into TTS text: %q", got)
	}
	if !strings.Contains(got, "that's it, baby.") {
		t.Errorf("speech dropped: %q", got)
	}
	found := false
	for _, phrase := range vocalPhrases[vocalMoan] {
		if strings.Contains(got, phrase) {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected a moan onomatopoeia in %q, want one of %v", got, vocalPhrases[vocalMoan])
	}
	if expandVocalTags("[laugh]", nil) != vocalPhrases[vocalLaugh][0] {
		t.Errorf("tag-only laugh = %q", expandVocalTags("[laugh]", nil))
	}
	if !hasVocalTag("[kiss] come here.") {
		t.Fatal("[kiss] must match so it can be stripped; kisses? does not match kiss")
	}
	if got := expandVocalTags("[kiss] come here.", nil); got != "come here." {
		t.Errorf("kiss should be dropped, not spoken: %q", got)
	}
}

func TestRunTurnMoanTagDoesNotSendMarkupToTTS(t *testing.T) {
	ttsProvider := &fakeTTS{}
	player := newFakePlayer(time.Millisecond)
	defer player.Close()
	p := &Pipeline{
		STT:    &fakeSTT{text: "hello"},
		Brain:  &fakeBrain{chunks: []string{"[moan] that's it, baby."}},
		TTS:    ttsProvider,
		Player: player,
		Events: events.NewBus(),
	}
	p.EnableVocals("")

	if _, err := p.RunTurn(context.Background()); err != nil {
		t.Fatalf("RunTurn() error = %v", err)
	}
	texts := ttsProvider.Texts()
	joined := strings.Join(texts, " ")
	if strings.Contains(strings.ToLower(joined), "moan") {
		t.Errorf("TTS received markup: %v", texts)
	}
	if !strings.Contains(joined, "that's it, baby.") {
		t.Errorf("TTS missed the speech: %v", texts)
	}
	found := false
	for _, phrase := range vocalPhrases[vocalMoan] {
		if strings.Contains(joined, phrase) {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("TTS should receive a moan onomatopoeia in the same take, got %v", texts)
	}
	if len(texts) != 1 {
		t.Errorf("want one TTS take so the moan is in her voice with the sentence, got %v", texts)
	}
}
