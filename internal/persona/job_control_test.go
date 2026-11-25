package persona

import (
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

func TestBrainJobControlOverridesTheInstallationSetting(t *testing.T) {
	config.SetConfigDirForTest(t, t.TempDir())
	for _, tc := range []struct {
		name    string
		global  bool
		persona *bool
		want    bool
	}{
		{"inherits off", false, nil, false},
		{"inherits on", true, nil, true},
		{"turns it on", false, new(true), true},
		{"turns it off", true, new(false), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.Config{BrainProvider: "ollama", VoiceJobControl: tc.global}
			p := &Profile{ID: "robin", Brain: Brain{JobControl: tc.persona}}
			claimed := slices.Contains(OverriddenKeys(cfg, p), "voice_job_control")
			Apply(cfg, p)
			if cfg.VoiceJobControl != tc.want || claimed != (tc.persona != nil) {
				t.Fatalf("voice_job_control = %t (claimed %t)", cfg.VoiceJobControl, claimed)
			}
		})
	}
}

// The stack editors do not show job control, so saving the stack keeps it,
// and a profile written with it reads it back.
func TestUpdateStackKeepsJobControl(t *testing.T) {
	setConfigDir(t, t.TempDir())
	if err := Write(&Profile{Schema: Schema, ID: "robin", DisplayName: "Robin", Brain: Brain{JobControl: new(true)},
		TTS: TTS{Provider: "kokoro", Voice: "af_heart"}, Prompts: PromptRefs{Persona: "robin"}}, false); err != nil {
		t.Fatal(err)
	}
	if _, err := UpdateStack("robin", Brain{Provider: "ollama", Model: "fixture"}, TTS{Provider: "kokoro", Voice: "af_heart"}); err != nil {
		t.Fatal(err)
	}
	p, err := Load("robin")
	if err != nil {
		t.Fatal(err)
	}
	if p.Brain.JobControl == nil || !*p.Brain.JobControl || p.Brain.Model != "fixture" {
		t.Fatalf("brain = %+v", p.Brain)
	}
	data, err := os.ReadFile(p.Path)
	if err != nil || !strings.Contains(string(data), "job_control: true") {
		t.Fatalf("persona.yaml = %s (%v)", data, err)
	}
}

// The persona form sets job control on its own, and choosing the default
// clears it rather than keeping the saved value as UpdateStack does.
func TestUpdateJobControlSetsAndClears(t *testing.T) {
	setConfigDir(t, t.TempDir())
	if err := Write(&Profile{Schema: Schema, ID: "lumen", DisplayName: "Lumen", Brain: Brain{Work: config.WorkInline, JobControl: new(true)},
		TTS: TTS{Provider: "kokoro", Voice: "af_heart"}, Prompts: PromptRefs{Persona: "lumen"}}, false); err != nil {
		t.Fatal(err)
	}
	read := func() (*Profile, string) {
		t.Helper()
		p, err := Load("lumen")
		if err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(p.Path)
		if err != nil {
			t.Fatal(err)
		}
		return p, string(data)
	}
	if _, err := UpdateJobControl("lumen", nil); err != nil {
		t.Fatal(err)
	}
	p, yaml := read()
	if p.Brain.JobControl != nil || strings.Contains(yaml, "job_control") || !strings.Contains(yaml, "work: inline") {
		t.Fatalf("cleared: brain %+v\n%s", p.Brain, yaml)
	}
	if _, err := UpdateJobControl("lumen", new(false)); err != nil {
		t.Fatal(err)
	}
	p, yaml = read()
	if p.Brain.JobControl == nil || *p.Brain.JobControl || !strings.Contains(yaml, "job_control: false") {
		t.Fatalf("off: brain %+v\n%s", p.Brain, yaml)
	}
	cfg := &config.Config{BrainProvider: "ollama", VoiceJobControl: true}
	Apply(cfg, p)
	if !config.WorksInline(cfg) || cfg.VoiceJobControl {
		t.Fatalf("bound: work %q, job control %v", config.WorkOf(cfg), cfg.VoiceJobControl)
	}
}

func TestCreateWithOptsKeepsInlineWorkAndJobControl(t *testing.T) {
	setConfigDir(t, t.TempDir())
	p, err := CreateWithOpts(&config.Config{BrainProvider: "ollama"}, CreateOpts{
		DisplayName: "Lumen",
		Brain:       Brain{Work: "Inline", JobControl: new(false)},
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := Load(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Brain.Work != config.WorkInline || got.Brain.JobControl == nil || *got.Brain.JobControl {
		t.Fatalf("created brain = %+v", got.Brain)
	}
}
