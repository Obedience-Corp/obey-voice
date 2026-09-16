package main

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/work"
)

func TestLoadStockScenarios(t *testing.T) {
	t.Parallel()
	dir := filepath.Join("scenarios")
	for _, name := range []string{"search-web-intent.yaml", "chat-then-work.yaml", "job-status.yaml", "live-router-spawn.yaml", "self-test.yaml", "persona-proof.yaml", "work-survival.yaml"} {
		scn, err := loadScenario(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if scn.Name == "" || len(scn.Turns) == 0 {
			t.Fatalf("%s: empty", name)
		}
	}
}

func TestScenarioInterruptionStimulusDelay(t *testing.T) {
	const base = `name: interrupt
input: speech
capture: {audio: true, speakers: true, microphone: true}
turns:
  - id: interrupt
    op: acoustic_interrupt
    wav: /initial.wav
    interrupt_wav: /interrupt.wav
    expected_transcript: Stop now.
`
	for _, tc := range []struct {
		name, field string
		want        time.Duration
		explicit    bool
	}{
		{"omitted", "", 900 * time.Millisecond, false},
		{"zero", "    stimulus_delay: 0s\n", 0, true},
		{"numeric-zero", "    stimulus_delay: 0\n", 0, true},
		{"custom", "    stimulus_delay: 125ms\n", 125 * time.Millisecond, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			scn, err := loadScenarioString("interrupt.yaml", base+tc.field)
			if err != nil {
				t.Fatal(err)
			}
			turn := scn.Turns[0]
			if got := turn.interruptionStimulusDelay(); got != tc.want || (turn.StimulusDelay != nil) != tc.explicit {
				t.Fatalf("delay=%s explicit=%t, want %s explicit=%t", got, turn.StimulusDelay != nil, tc.want, tc.explicit)
			}
		})
	}
	for name, body := range map[string]string{
		"negative":   base + "    stimulus_delay: -1ms\n",
		"completion": strings.Replace(strings.Replace(base, "acoustic_interrupt", "acoustic_completion", 1), "    interrupt_wav: /interrupt.wav\n", "", 1) + "    stimulus_delay: 0s\n",
		"ordinary":   "name: chat\nturns:\n  - id: chat\n    user: hello\n    stimulus_delay: 0s\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := loadScenarioString("bad.yaml", body); err == nil || !strings.Contains(err.Error(), "stimulus_delay requires") {
				t.Fatalf("expected stimulus_delay validation error, got %v", err)
			}
		})
	}
}

func TestScenarioWorkProofOperationsAndFrozenOutcomeExpectations(t *testing.T) {
	scn, err := loadScenarioString("work.yaml", `name: work
turns:
  - id: start
    user: "research this local fixture"
    wait: job_started
  - id: switch
    op: select_persona
    persona: santa
    wait: idle
    expect:
      - work_running_at_selection: true
      - work_completed_before_selection: false
      - work_origin_persona: samantha
      - work_current_responder: santa
  - id: deliver
    op: run_background
    wait: work_durable
    expect:
      - work_pending: false
      - work_communicated: true
      - work_receipt: true
      - work_persisted: true
`)
	if err != nil {
		t.Fatal(err)
	}
	if scn.Turns[1].Op != "select_persona" || scn.Turns[2].Wait != waitWorkDurable {
		t.Fatalf("work sequence = %+v", scn.Turns)
	}
	scope := work.WorkScope{SessionID: "session", RuntimeEpoch: "runtime", ConversationEpoch: "epoch"}
	snapshot := work.Snapshot{Scope: scope, Revision: 3, PersistedRevision: 3,
		Jobs:         []work.JobView{{State: work.JobTerminal}},
		Communicated: []work.ResultView{{Outcome: work.Outcome{Config: map[string]string{"persona": "samantha"}}, LastAttempt: &work.DeliveryEvidence{Receipt: work.DeliveryReceipt{Mode: "voice", ResponseCommitted: true, PlaybackComplete: true, Audible: true, Segments: 1}}}}}
	if got := workOriginPersona(snapshot); got != "samantha" || workState(snapshot) != "communicated" || !workHasReceipt(snapshot) {
		t.Fatalf("outcome projection origin=%q state=%q receipt=%t", got, workState(snapshot), workHasReceipt(snapshot))
	}
}

func TestScenarioSelectionSchemaRejectsUnknownOpAndMisplacedError(t *testing.T) {
	t.Parallel()
	if _, err := loadScenarioString("bad-op.yaml", "name: x\nturns:\n  - id: a\n    op: select_santa\n"); err == nil {
		t.Fatal("expected unknown op error")
	}
	if _, err := loadScenarioString("bad-error.yaml", "name: x\nturns:\n  - id: a\n    expect_error: nope\n"); err == nil {
		t.Fatal("expected misplaced expect_error error")
	}
}

func TestLoadRejectsUnknownKeys(t *testing.T) {
	t.Parallel()
	if _, err := loadScenarioString("bad.yaml", "name: x\nturns: []\nbogus: 1\n"); err == nil {
		t.Fatal("expected unknown field error")
	}
}

func TestEmptyUserRequiresTerminalOrIdleWait(t *testing.T) {
	t.Parallel()
	if _, err := loadScenarioString("bad.yaml", "name: x\nturns:\n  - id: a\n    user: \"\"\n    wait: job_started\n"); err == nil {
		t.Fatal("expected empty-user wait error")
	}
}

func TestScenarioAcousticCompletion(t *testing.T) {
	const scenario = `name: physical-completion
input: speech
capture: {audio: true, speakers: true, microphone: true}
turns:
  - id: completion
    op: acoustic_completion
    wav: /fixture.wav
    expected_transcript: Say hello.
`
	if _, err := loadScenarioString("completion.yaml", scenario); err != nil {
		t.Fatal(err)
	}
}

func TestScenarioInterruptionCompletionWindow(t *testing.T) {
	const base = "name: bounded\ninput: speech\ncapture: {microphone: true, speakers: true}\nturns:\n  - id: early\n    op: acoustic_interrupt\n    wav: /initial.wav\n    interrupt_wav: /wait.wav\n    expected_transcript: Wait.\n"
	scn, err := loadScenarioString("bounded.yaml", base+"    stimulus_complete_within: 600ms\n")
	if err != nil {
		t.Fatal(err)
	}
	if scn.Turns[0].interruptionStimulusOptions().completeWithin != 600*time.Millisecond {
		t.Fatal("completion window not retained")
	}
	for _, value := range []string{"0s", "-1ms"} {
		if _, err := loadScenarioString("bad.yaml", base+"    stimulus_complete_within: "+value+"\n"); err == nil {
			t.Fatalf("accepted invalid window %s", value)
		}
	}
	if _, err := loadScenarioString("misplaced.yaml", "name: x\nturns:\n  - id: x\n    user: hello\n    stimulus_complete_within: 600ms\n"); err == nil {
		t.Fatal("accepted completion window on ordinary turn")
	}
}

func TestScenarioDecodeEvidenceRequiresUntimedOrdinaryMicrophoneWAV(t *testing.T) {
	const valid = `name: decoder-evidence
input: speech
capture: {audio: true, speakers: true, microphone: true, stt_decodes: true}
turns:
  - id: near
    wav: /fixture.wav
    expected_transcript: Wait.
`
	scenario, err := loadScenarioString("valid", valid)
	if err != nil || !scenario.Capture.STTDecodes {
		t.Fatalf("valid decoder scenario: %+v %v", scenario, err)
	}
	for name, body := range map[string]string{
		"no microphone":     strings.Replace(valid, "microphone: true", "microphone: false", 1),
		"baseline latency":  valid + "latency_condition: baseline\n",
		"worker latency":    valid + "latency_condition: worker\n",
		"control operation": valid + "    op: acoustic_completion\n",
		"missing WAV":       strings.Replace(valid, "    wav: /fixture.wav\n", "", 1),
		"interrupt timer":   valid + "    interrupt_after: 1s\n",
		"latency iteration": valid + "    latency_iteration: 1\n",
		"excluded warmup":   valid + "    latency_warmup: true\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := loadScenarioString(name, body); err == nil || !strings.Contains(err.Error(), "stt_decodes") {
				t.Fatalf("invalid decoder scenario accepted/wrong rejection: %v", err)
			}
		})
	}
}
