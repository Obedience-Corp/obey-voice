package pipeline

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/tts"
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
		{"[AHH] yes", []vocalPart{{sound: vocalAhh}, {text: "yes"}}},
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
	got := expandVocalTags("[laugh] that's it, baby.", nil)
	if strings.Contains(strings.ToLower(got), "[laugh]") {
		t.Errorf("markup leaked into TTS text: %q", got)
	}
	if !strings.Contains(got, "that's it, baby.") {
		t.Errorf("speech dropped: %q", got)
	}
	if expandVocalTags("[laugh]", nil) != vocalPhrases[vocalLaugh][0] {
		t.Errorf("tag-only laugh = %q", expandVocalTags("[laugh]", nil))
	}
	if got := expandVocalTags("[moan] that's it, baby.", nil); got != "that's it, baby." {
		t.Errorf("moan has no working spelling and must strip, got %q", got)
	}
	if got := expandVocalTags("[moan]", nil); got != "" {
		t.Errorf("tag-only moan must not send Nnngh/Ahhn, got %q", got)
	}
	if !hasVocalTag("[kiss] come here.") {
		t.Fatal("[kiss] must match so it can be stripped; kisses? does not match kiss")
	}
	if got := expandVocalTags("[kiss] come here.", nil); got != "come here." {
		t.Errorf("kiss should be dropped, not spoken: %q", got)
	}
}

func TestRunTurnMoanTagDoesNotSendMarkupToTTS(t *testing.T) {
	ttsProvider := &qwenLikeTTS{fakeTTS: fakeTTS{vocalTags: true}}
	player := newFakePlayer(time.Millisecond)
	defer player.Close()
	p := &Pipeline{
		STT:    &fakeSTT{text: "hello"},
		Brain:  &fakeBrain{chunks: []string{"[laugh] that's it, baby."}},
		TTS:    ttsProvider,
		Player: player,
		Events: events.NewBus(),
	}
	p.EnableVocals("")

	if _, err := p.RunTurn(t.Context()); err != nil {
		t.Fatalf("RunTurn() error = %v", err)
	}
	texts := ttsProvider.Texts()
	joined := strings.Join(texts, " ")
	if strings.Contains(strings.ToLower(joined), "[laugh]") {
		t.Errorf("TTS received markup: %v", texts)
	}
	if !strings.Contains(joined, "that's it, baby.") {
		t.Errorf("TTS missed the speech: %v", texts)
	}
	found := false
	for _, phrase := range vocalPhrases[vocalLaugh] {
		if strings.Contains(joined, phrase) {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("TTS should receive a laugh onomatopoeia in the same take, got %v", texts)
	}
	if len(texts) != 1 {
		t.Errorf("want one TTS take so the laugh is in her voice with the sentence, got %v", texts)
	}
}

func TestRunTurnMoanTagIsStrippedNotSpoken(t *testing.T) {
	ttsProvider := &qwenLikeTTS{fakeTTS: fakeTTS{vocalTags: true}}
	player := newFakePlayer(time.Millisecond)
	defer player.Close()
	p := &Pipeline{
		STT:    &fakeSTT{text: "hello"},
		Brain:  &fakeBrain{chunks: []string{"[moan] that's it, baby."}},
		TTS:    ttsProvider,
		Player: player,
		Events: events.NewBus(),
	}
	if _, err := p.RunTurn(t.Context()); err != nil {
		t.Fatalf("RunTurn() error = %v", err)
	}
	joined := strings.Join(ttsProvider.Texts(), " ")
	for _, bad := range []string{"moan", "Nnngh", "Ahhn", "Haaaah", "ming"} {
		if strings.Contains(joined, bad) {
			t.Errorf("moan spelling leaked to TTS (%q) in %q", bad, joined)
		}
	}
	if !strings.Contains(joined, "that's it, baby.") {
		t.Errorf("TTS missed the speech: %v", ttsProvider.Texts())
	}
}

func TestPrepareSpeechForTTSDependsOnEngine(t *testing.T) {
	qwen := &qwenLikeTTS{fakeTTS: fakeTTS{vocalTags: true}}
	kokoro := &fakeTTS{}
	native := &fakeTTS{vocalTags: true}
	in := "[sigh] hello there."
	got := prepareSpeechForTTS(in, qwen, nil, false)
	if strings.Contains(strings.ToLower(got), "[sigh]") {
		t.Errorf("Qwen still saw markup: %q", got)
	}
	if !strings.Contains(got, "hello there.") {
		t.Errorf("Qwen dropped speech: %q", got)
	}
	found := false
	for _, phrase := range vocalPhrases[vocalSigh] {
		if strings.Contains(got, phrase) {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("Qwen should get sigh onomatopoeia, got %q", got)
	}
	got = prepareSpeechForTTS(in, kokoro, nil, false)
	if got != "hello there." {
		t.Errorf("Kokoro = %q, want stripped speech only", got)
	}
	if got := prepareSpeechForTTS("[sigh]", kokoro, nil, false); got != "" {
		t.Errorf("Kokoro tag-only = %q, want empty", got)
	}
	if got := prepareSpeechForTTS(in, native, nil, false); got != in {
		t.Errorf("native-tag engine = %q, want tags passed through", got)
	}
	if got := prepareSpeechForTTS("[mouth] hello there.", qwen, nil, false); got != "hello there." {
		t.Errorf("Qwen must not speak [mouth], got %q", got)
	}
	if got := prepareSpeechForTTS("[mouth] hello there.", kokoro, nil, false); got != "hello there." {
		t.Errorf("Kokoro must not speak [mouth], got %q", got)
	}
}

func TestRunTurnUnknownTagDoesNotDropRecordedLaugh(t *testing.T) {
	dir := t.TempDir()
	samples := make([]float32, 2400)
	for i := range samples {
		samples[i] = 0.1
	}
	if err := audio.WriteWAVFloat32(filepath.Join(dir, "laugh.wav"), vocalSampleRate, samples); err != nil {
		t.Fatal(err)
	}
	ttsProvider := &fakeTTS{}
	player := newFakePlayer(time.Millisecond)
	defer player.Close()
	p := &Pipeline{
		STT:    &fakeSTT{text: "hello"},
		Brain:  &fakeBrain{chunks: []string{"[mouth] [laugh] hello"}},
		TTS:    ttsProvider,
		Player: player,
		Events: events.NewBus(),
	}
	p.EnableVocals(dir)
	if _, err := p.RunTurn(t.Context()); err != nil {
		t.Fatalf("RunTurn() error = %v", err)
	}
	joined := strings.Join(ttsProvider.Texts(), " ")
	if strings.Contains(strings.ToLower(joined), "mouth") {
		t.Errorf("TTS saw mouth: %v", ttsProvider.Texts())
	}
	if strings.Contains(strings.ToLower(joined), "laugh") || strings.Contains(joined, "Haha") {
		t.Errorf("recorded/onomatopoeia laugh must not be mixed in: %v", ttsProvider.Texts())
	}
	if !strings.Contains(joined, "hello") {
		t.Errorf("TTS missed hello: %v", ttsProvider.Texts())
	}
}

func TestRunTurnMouthTagIsNotSpoken(t *testing.T) {
	ttsProvider := &fakeTTS{}
	player := newFakePlayer(time.Millisecond)
	defer player.Close()
	p := &Pipeline{
		STT:    &fakeSTT{text: "hello"},
		Brain:  &fakeBrain{chunks: []string{"[mouth] hello there."}},
		TTS:    ttsProvider,
		Player: player,
		Events: events.NewBus(),
	}
	if _, err := p.RunTurn(t.Context()); err != nil {
		t.Fatalf("RunTurn() error = %v", err)
	}
	joined := strings.Join(ttsProvider.Texts(), " ")
	if strings.Contains(strings.ToLower(joined), "mouth") {
		t.Errorf("TTS spoke the stage tag: %v", ttsProvider.Texts())
	}
	if !strings.Contains(joined, "hello there.") {
		t.Errorf("TTS missed the speech: %v", ttsProvider.Texts())
	}
}

// The textual join is the caller's decision. A non-joining engine speaks the
// text it was handed unless the turn loop says this string is a group it
// assembled around a held beat — otherwise every announcement and every whole
// text-mode reply gets its sentence periods rewritten into commas.
func TestPrepareSpeechForTTSJoinsOnlyWhenTheLoopSaysSo(t *testing.T) {
	kokoro := &fakeTTS{}
	const group = "Wait. Wait, you what?"

	if got := prepareSpeechForTTS(group, kokoro, nil, false); got != group {
		t.Errorf("prepareSpeechForTTS(joinBeats=false) = %q, want the text as written", got)
	}
	if got, want := prepareSpeechForTTS(group, kokoro, nil, true), "Wait, wait, you what?"; got != want {
		t.Errorf("prepareSpeechForTTS(joinBeats=true) = %q, want %q", got, want)
	}

	const reply = "The build finished on main. It passed. Tests are green across the board."
	if got := prepareSpeechForTTS(reply, kokoro, nil, false); got != reply {
		t.Errorf("a whole reply was rewritten: %q", got)
	}
}

func TestPrepareSpeechForTTSRotatesOnomatopoeiaWhenBankPresent(t *testing.T) {
	qwen := &qwenLikeTTS{fakeTTS: fakeTTS{vocalTags: true}}
	bank := newVocalBank("")
	seen := map[string]bool{}
	for range 40 {
		got := prepareSpeechForTTS("[sigh] hi", qwen, bank, false)
		for _, phrase := range vocalPhrases[vocalSigh] {
			if strings.Contains(got, phrase) {
				seen[phrase] = true
			}
		}
	}
	if len(seen) < 2 {
		t.Fatalf("vocal bank should rotate sigh phrases, got %v", seen)
	}
}

func TestRunTurnStripsVocalTagsWhenEngineCannotRenderThem(t *testing.T) {
	ttsProvider := &fakeTTS{}
	player := newFakePlayer(time.Millisecond)
	defer player.Close()
	p := &Pipeline{
		STT:    &fakeSTT{text: "hello"},
		Brain:  &fakeBrain{chunks: []string{"[sigh] that's it, baby."}},
		TTS:    ttsProvider,
		Player: player,
		Events: events.NewBus(),
	}
	p.EnableVocals("")

	if _, err := p.RunTurn(t.Context()); err != nil {
		t.Fatalf("RunTurn() error = %v", err)
	}
	texts := ttsProvider.Texts()
	joined := strings.Join(texts, " ")
	if strings.Contains(strings.ToLower(joined), "sigh") || strings.Contains(joined, "Hahh") || strings.Contains(joined, "Haaah") {
		t.Errorf("unsupported engine received vocalization: %v", texts)
	}
	if !strings.Contains(joined, "that's it, baby.") {
		t.Errorf("TTS missed the speech: %v", texts)
	}
}

func TestRunTurnSkipsTagOnlyWhenEngineCannotRenderThem(t *testing.T) {
	ttsProvider := &fakeTTS{}
	player := newFakePlayer(time.Millisecond)
	defer player.Close()
	p := &Pipeline{
		STT:    &fakeSTT{text: "hello"},
		Brain:  &fakeBrain{chunks: []string{"[sigh]"}},
		TTS:    ttsProvider,
		Player: player,
		Events: events.NewBus(),
	}
	if _, err := p.RunTurn(t.Context()); err != nil {
		t.Fatalf("RunTurn() error = %v", err)
	}
	if texts := ttsProvider.Texts(); len(texts) != 0 {
		t.Errorf("tag-only beat on Kokoro must not call TTS, got %v", texts)
	}
}

type qwenLikeTTS struct{ fakeTTS }

func (q *qwenLikeTTS) RewriteVocalTags(s string, pick func(kind string) string) string {
	return expandVocalTags(s, pick)
}

// Qwen carries prosody across a request, so its text is never rewritten.
func (q *qwenLikeTTS) JoinsSentences() bool { return true }

func TestFallbackKokoroDoesNotReceiveQwenOnomatopoeia(t *testing.T) {
	primary := &failingTTS{err: &tts.ProviderError{Kind: tts.ProviderErrorWorker, Err: errors.New("worker down")}}
	primary.vocalTags = true
	fallback := &fakeTTS{}
	player := newFakePlayer(time.Millisecond)
	defer player.Close()
	p := &Pipeline{
		STT:         &fakeSTT{text: "hello"},
		Brain:       &fakeBrain{chunks: []string{"[laugh] nice."}},
		TTS:         primary,
		TTSFallback: fallback,
		Player:      player,
		Events:      events.NewBus(),
	}
	if _, err := p.RunTurn(t.Context()); err != nil {
		t.Fatalf("RunTurn() error = %v", err)
	}
	if got := strings.Join(fallback.Texts(), " "); got != "nice." {
		t.Errorf("Kokoro fallback = %q, want stripped speech", got)
	}
	for _, phrase := range vocalPhrases[vocalLaugh] {
		if strings.Contains(strings.Join(fallback.Texts(), " "), phrase) {
			t.Errorf("fallback received Qwen onomatopoeia %q in %v", phrase, fallback.Texts())
		}
	}
}

type failingTTS struct {
	fakeTTS
	err error
}

func (f *failingTTS) Synthesize(context.Context, string) (*audio.PCMStream, error) {
	return nil, f.err
}

// Only the orphan a stripped tag leaves at the front goes. Punctuation further
// in is the speaker's: a spaced " , " is a pause and a trailing "?" is the
// sentence type the engine reads as a contour.
func TestTrimSpeechResidueKeepsPauses(t *testing.T) {
	cases := map[string]string{
		". The build finished.":      "The build finished.",
		". . Still finished.":        "Still finished.",
		"Well ... I guess so.":       "Well ... I guess so.",
		"Wait , what ... happened ?": "Wait , what ... happened ?",
		"Nothing to trim here.":      "Nothing to trim here.",
		".":                          "",
	}
	for in, want := range cases {
		if got := trimSpeechResidue(in); got != want {
			t.Errorf("trimSpeechResidue(%q) = %q, want %q", in, got, want)
		}
	}
}

// The orphan a tag leaves mid-utterance is removed where the tag was, because
// only there is that period known to be the tag's. Merged groups put tags
// inside the text, not only at its front.
func TestStripSpokenVocalTagsTakesTheOrphanPunctuation(t *testing.T) {
	cases := map[string]string{
		"Okay. [laugh]. Good.":         "Okay. Good.",
		"[laugh]. The build finished.": "The build finished.",
		"[sigh]... I guess so.":        "... I guess so.",
		"Nothing to strip here.":       "Nothing to strip here.",
	}
	for in, want := range cases {
		if got := stripSpokenVocalTags(in); got != want {
			t.Errorf("stripSpokenVocalTags(%q) = %q, want %q", in, got, want)
		}
	}
}

// B3: an engine that cannot vocalize a tag gets the tag's orphan punctuation
// removed where the tag was, not only at the front of the utterance. A merged
// group puts tags mid-text, and plain stripVocalTags leaves "Okay. . Good."
// for the engine to read the bare period aloud as "dot".
func TestPrepareSpeechForTTSRemovesTheOrphanATagLeaves(t *testing.T) {
	// Strips tags (no SupportsVocalTags) and joins sentences, so the result is
	// the rendering and nothing else.
	stripping := &qwenLikeTTS{}
	cases := map[string]string{
		"[laugh]. The build finished.": "The build finished.",
		"Okay. [laugh]. Good.":         "Okay. Good.",
		"[sigh]... I guess so.":        "... I guess so.",
	}
	for in, want := range cases {
		if got := prepareSpeechForTTS(in, stripping, nil, false); got != want {
			t.Errorf("prepareSpeechForTTS(%q) = %q, want %q", in, got, want)
		}
	}
}
