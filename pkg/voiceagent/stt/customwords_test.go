package stt

import (
	"reflect"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/stt/correct"
)

func TestCustomWordsFromConfig(t *testing.T) {
	cfg := &config.Config{
		STTCustomWords:             []string{"Claude=clawed", "Ollama!", "", "!!!"},
		STTCustomWordsFromPersonas: true,
		AgentName:                  "Obey",
		PersonaDisplayNames:        []string{"Obey", "Samantha", "Santa"},
	}
	cw, report := CustomWordsFromConfig(cfg)

	var texts []string
	for _, term := range report.Terms {
		texts = append(texts, term.Text)
	}
	if want := []string{"Claude", "Ollama", "Obey", "Samantha", "Santa"}; !reflect.DeepEqual(texts, want) {
		t.Fatalf("terms = %v, want %v", texts, want)
	}
	if want := []string{"", "!!!"}; !reflect.DeepEqual(report.Ignored, want) {
		t.Fatalf("ignored = %q, want %q", report.Ignored, want)
	}
	if want := []string{"Ollama", "Samantha"}; !reflect.DeepEqual(report.Fuzzy, want) {
		t.Fatalf("fuzzy = %v, want %v", report.Fuzzy, want)
	}
	if report.Threshold != correct.DefaultThreshold || report.Dropped != 0 {
		t.Fatalf("report = %+v", report)
	}
	got, _, _ := cw.Scan("ask clawed and sam antha on olama")
	if want := "ask Claude and Samantha on Ollama"; got != want {
		t.Fatalf("Scan = %q, want %q", got, want)
	}
}

func TestCustomWordsFromConfigThreshold(t *testing.T) {
	cfg := &config.Config{STTCustomWords: []string{"Kokoro!"}, STTWordCorrectionThreshold: 0.5}
	cw, report := CustomWordsFromConfig(cfg)
	if report.Threshold != 0.5 {
		t.Fatalf("threshold = %v", report.Threshold)
	}
	if got, _, _ := cw.Scan("use coco row"); got != "use Kokoro" {
		t.Fatalf("Scan = %q", got)
	}
}

func TestWithConfiguredCorrection(t *testing.T) {
	p := &fakeProvider{}
	if got := withConfiguredCorrection(p, &config.Config{}); got != Provider(p) {
		t.Fatal("no terms should leave the provider unwrapped")
	}
	if got := withConfiguredCorrection(p, &config.Config{STTCustomWordsFromPersonas: true}); got != Provider(p) {
		t.Fatal("persona names off and none listed should leave the provider unwrapped")
	}
	if got := withConfiguredCorrection(p, &config.Config{STTCustomWords: []string{"Samantha"}}); got == Provider(p) {
		t.Fatal("a term should wrap the provider")
	}
	if got := withConfiguredCorrection(p, &config.Config{STTCustomWordsFromPersonas: true, AgentName: "Obey"}); got == Provider(p) {
		t.Fatal("the agent name should wrap the provider")
	}
}
