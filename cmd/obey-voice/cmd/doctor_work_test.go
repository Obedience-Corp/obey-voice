package cmd

import (
	"strings"
	"testing"

	"github.com/Obedience-Corp/obey-voice/internal/persona"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

func writeWorkPersona(t *testing.T, id string, b persona.Brain) {
	t.Helper()
	if err := persona.Write(&persona.Profile{Schema: persona.Schema, ID: id, DisplayName: id, Brain: b, Prompts: persona.PromptRefs{Persona: id}}, false); err != nil {
		t.Fatal(err)
	}
}

func doctorLine(t *testing.T, out, name string) string {
	t.Helper()
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "] "+name+":") {
			return strings.TrimSpace(line)
		}
	}
	t.Fatalf("no %q line in:\n%s", name, out)
	return ""
}

// Doctor says, per persona, who runs its background work and why, so the
// default each persona takes is visible.
func TestDoctorSaysWhoDoesEachPersonasWork(t *testing.T) {
	config.SetConfigDirForTest(t, t.TempDir())
	writeWorkPersona(t, "mira", persona.Brain{Provider: "claude", Model: "haiku"})
	writeWorkPersona(t, "ember", persona.Brain{Provider: "ollama", Model: "persona:14b"})
	writeWorkPersona(t, "rex", persona.Brain{Provider: "claude", Worker: persona.Worker{Provider: "codex", Model: "gpt-5.5"}})
	writeWorkPersona(t, "ada", persona.Brain{Provider: "codex", Model: "gpt-5.5", Work: config.WorkWorker})
	writeWorkPersona(t, "kai", persona.Brain{Provider: "ollama", Model: "persona:14b", Work: config.WorkOwn})
	cfg := healthyBrain(&config.Config{VoiceWorkerProvider: "ollama", VoiceWorkerModel: "qwen3:8b"})

	out, err := runDoctorCmd(t, cfg, t.TempDir(), okLookPath, nil, false)
	if err != nil {
		t.Fatalf("doctor: %v\n%s", err, out)
	}
	for name, want := range map[string]string{
		"work persona mira":  "[OK  ] work persona mira: its own claude (haiku) does it for up to 15 minutes; default on claude",
		"work persona ember": "[OK  ] work persona ember: the worker ollama (qwen3:8b) does it; default on ollama",
		"work persona rex":   "[OK  ] work persona rex: the worker codex (gpt-5.5) does it; default: its brain.worker is set",
		"work persona ada":   "[OK  ] work persona ada: the worker ollama (qwen3:8b) does it; brain.work is worker",
		"work persona kai":   "[OK  ] work persona kai: its own ollama (persona:14b) does it; brain.work is own",
	} {
		if got := doctorLine(t, out, name); got != want {
			t.Errorf("%s:\n got %q\nwant %q", name, got, want)
		}
	}

	daily := int64(1_000_000)
	host := "http://127.0.0.1:11434"
	tiered := config.DefaultVoiceWorkers()
	tiered.Mode, tiered.Policy.ChargePolicy, tiered.Policy.DailyChargeMicroUSD = config.WorkerModeTiered, "strict", &daily
	tiered.Tiers.Simple.Enabled = true
	tiered.Tiers.Simple.Assignment = &config.WorkerAssignment{Provider: "ollama", Model: "first:7b", EndpointRef: &host, Billing: config.WorkerBillingLocal}
	cfg.VoiceWorkers = tiered
	out, _ = runDoctorCmd(t, cfg, t.TempDir(), okLookPath, nil, false)
	if got := doctorLine(t, out, "work persona mira"); !strings.HasPrefix(got, "[WARN] work persona mira: its own claude (haiku) does it for up to 15 minutes or its tier's runtime if shorter; default on claude; refused under the strict worker policy") ||
		!strings.Contains(out, "persona edit mira --work worker") {
		t.Errorf("strict mira = %q", got)
	}
	if got := doctorLine(t, out, "work persona kai"); got != "[OK  ] work persona kai: its own ollama (persona:14b) does it within its tier's runtime; brain.work is own" {
		t.Errorf("strict kai = %q", got)
	}
	if got := doctorLine(t, out, "work persona ember"); got != "[OK  ] work persona ember: the worker tiers do it; default on ollama" {
		t.Errorf("tiered ember = %q", got)
	}

	tiered.Policy.ChargePolicy, tiered.Policy.DailyChargeMicroUSD = "advisory", nil
	tiered.Tiers.Simple.Enabled = false
	out, _ = runDoctorCmd(t, cfg, t.TempDir(), okLookPath, nil, false)
	if got := doctorLine(t, out, "work persona kai"); got != "[WARN] work persona kai: its own ollama (persona:14b) does it within its tier's runtime; brain.work is own; refused: worker tiers are on and none is enabled" {
		t.Errorf("closed tiers kai = %q", got)
	}
	cfg.VoiceWorkerJobBudget = config.UnlimitedJobBudget
	tiered.Tiers.Simple.Enabled = true
	out, _ = runDoctorCmd(t, cfg, t.TempDir(), okLookPath, nil, false)
	if got := doctorLine(t, out, "work persona mira"); got != "[OK  ] work persona mira: its own claude (haiku) does it within its tier's runtime; default on claude" {
		t.Errorf("tiered unlimited mira = %q", got)
	}

	cfg.VoiceWorkers, cfg.VoiceWorkerJobBudget = nil, config.UnlimitedJobBudget
	out, _ = runDoctorCmd(t, cfg, t.TempDir(), okLookPath, nil, false)
	if got := doctorLine(t, out, "work persona mira"); got != "[WARN] work persona mira: its own claude (haiku) does it with no time limit (voice_worker_job_budget is -1); default on claude" ||
		!strings.Contains(out, "or to 0 for the 15-minute default") {
		t.Errorf("unlimited mira = %q", got)
	}
	if got := doctorLine(t, out, "work persona kai"); got != "[OK  ] work persona kai: its own ollama (persona:14b) does it; brain.work is own" {
		t.Errorf("unlimited kai = %q", got)
	}
	cfg.VoiceWorkerJobBudget = 600
	out, _ = runDoctorCmd(t, cfg, t.TempDir(), okLookPath, nil, false)
	if got := doctorLine(t, out, "work persona mira"); got != "[OK  ] work persona mira: its own claude (haiku) does it for up to 600s; default on claude" {
		t.Errorf("budgeted mira = %q", got)
	}

	cfg.VoiceWorkerJobBudget, cfg.VoiceWorkEnabled = 0, true
	out, _ = runDoctorCmd(t, cfg, t.TempDir(), okLookPath, nil, false)
	if got := doctorLine(t, out, "work persona mira"); !strings.Contains(got, "the worker ollama (qwen3:8b) does it; default on claude, but voice_work_enabled keeps a hosted persona's work on the local worker") {
		t.Errorf("work inbox mira = %q", got)
	}
}
