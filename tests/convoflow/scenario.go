package main

import (
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type waitKind string

const (
	waitIdle        waitKind = "idle"
	waitJobStarted  waitKind = "job_started"
	waitJobTerminal waitKind = "job_terminal"
	waitWorkDurable waitKind = "work_durable"
)

// Scenario is one scripted conversation. Unknown YAML keys are load errors.
type Scenario struct {
	LatencyCondition string   `yaml:"latency_condition"`
	Name             string   `yaml:"name"`
	Description      string   `yaml:"description"`
	Duration         duration `yaml:"duration"`
	Input            string   `yaml:"input"` // text | speech
	RouterTools      string   `yaml:"router_tools"`
	TTS              struct {
		Provider string `yaml:"provider"`
		Voice    string `yaml:"voice"`
	} `yaml:"tts"`
	Capture struct {
		Microphone bool `yaml:"microphone"`
		Audio      bool `yaml:"audio"`
		Speakers   bool `yaml:"speakers"`
	} `yaml:"capture"`
	Turns   []Turn          `yaml:"turns"`
	Expect  []Expectation   `yaml:"expect"`
	Brain   scenarioBrain   `yaml:"brain"`
	Worker  scenarioWorker  `yaml:"worker"`
	Persona scenarioPersona `yaml:"persona"`
	// HistoryFixture seeds the owning provider through ImportHistory before
	// scripted turns. Compaction itself remains Pipeline-owned.
	HistoryFixture string `yaml:"history_fixture"`
}

type scenarioPersona struct {
	Initial string `yaml:"initial"`
}

type scenarioBrain struct {
	Provider string `yaml:"provider"`
	Model    string `yaml:"model"`
	Host     string `yaml:"host"`
}

type scenarioWorker struct {
	SustainedLoad bool   `yaml:"sustained_load"`
	Provider      string `yaml:"provider"`
	Model         string `yaml:"model"`
	Host          string `yaml:"host"`
}

type Turn struct {
	LatencyIteration       int           `yaml:"latency_iteration"`
	LatencyWarmup          bool          `yaml:"latency_warmup"`
	InterruptAfter         duration      `yaml:"interrupt_after"`
	StimulusDelay          *duration     `yaml:"stimulus_delay"`
	StimulusCompleteWithin *duration     `yaml:"stimulus_complete_within"`
	ID                     string        `yaml:"id"`
	User                   string        `yaml:"user"`
	Timeout                duration      `yaml:"timeout"`
	Duration               duration      `yaml:"duration"`
	Wait                   waitKind      `yaml:"wait"`
	Expect                 []Expectation `yaml:"expect"`
	WAV                    string        `yaml:"wav"`
	InterruptWAV           string        `yaml:"interrupt_wav"`
	ExpectedTranscript     string        `yaml:"expected_transcript"`
	Op                     string        `yaml:"op"`
	Persona                string        `yaml:"persona"`
	ExpectError            string        `yaml:"expect_error"`
}

type Expectation struct {
	Event                        string `yaml:"event"`
	NoEvent                      string `yaml:"no_event"`
	SpokenMatches                string `yaml:"spoken_matches"`
	SpokenNotMatches             string `yaml:"spoken_not_matches"`
	ResponseMatches              string `yaml:"response_matches"`
	LiveHistoryIncludes          string `yaml:"live_history_includes"`
	LiveHistoryExcludes          string `yaml:"live_history_excludes"`
	LiveRouterPolicy             string `yaml:"live_router_policy"`
	WorkerRouterPolicy           string `yaml:"worker_router_policy"`
	WorkerToolsEnabled           *bool  `yaml:"worker_tools_enabled"`
	WorkerCalls                  *int   `yaml:"worker_calls"`
	RouteKind                    string `yaml:"route_kind"`
	RouteSource                  string `yaml:"route_source"`
	Metric                       string `yaml:"metric"`
	Op                           string `yaml:"op"`
	Value                        int    `yaml:"value"`
	PersonaID                    string `yaml:"persona_id"`
	DisplayName                  string `yaml:"display_name"`
	ResponsePersonaID            string `yaml:"response_persona_id"`
	ResponseID                   string `yaml:"response_id"`
	ResponseVoice                string `yaml:"response_voice"`
	ResponseBrainModel           string `yaml:"response_brain_model"`
	SessionID                    string `yaml:"session_id"`
	NoLiveBrainCall              bool   `yaml:"no_live_brain_call"`
	BrainCalls                   *int   `yaml:"brain_calls"`
	CanonicalIncludes            string `yaml:"canonical_includes"`
	WorkOriginPersona            string `yaml:"work_origin_persona"`
	WorkCurrentResponder         string `yaml:"work_current_responder"`
	WorkState                    string `yaml:"work_state"`
	WorkPending                  *bool  `yaml:"work_pending"`
	WorkCommunicated             *bool  `yaml:"work_communicated"`
	WorkReceipt                  *bool  `yaml:"work_receipt"`
	WorkPersisted                *bool  `yaml:"work_persisted"`
	WorkRunningAtSelection       *bool  `yaml:"work_running_at_selection"`
	WorkCompletedBeforeSelection *bool  `yaml:"work_completed_before_selection"`
}

type duration struct{ time.Duration }

// interruptionStimulusDelay keeps omission distinct from an explicit zero.
func (t Turn) interruptionStimulusDelay() time.Duration {
	if t.StimulusDelay == nil {
		return 900 * time.Millisecond
	}
	return t.StimulusDelay.Duration
}

func (t Turn) interruptionStimulusOptions() interruptionStimulusOptions {
	options := interruptionStimulusOptions{delay: t.interruptionStimulusDelay()}
	if t.StimulusCompleteWithin != nil {
		options.completeWithin = t.StimulusCompleteWithin.Duration
	}
	return options
}

func (d *duration) UnmarshalYAML(node *yaml.Node) error {
	var s string
	if err := node.Decode(&s); err != nil {
		return err
	}
	if s == "" {
		d.Duration = 0
		return nil
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("bad duration %q: %w", s, err)
	}
	d.Duration = v
	return nil
}

func loadScenario(path string) (*Scenario, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return loadScenarioReader(path, f)
}

func loadScenarioReader(name string, r io.Reader) (*Scenario, error) {
	dec := yaml.NewDecoder(r)
	dec.KnownFields(true)
	var scn Scenario
	if err := dec.Decode(&scn); err != nil {
		return nil, fmt.Errorf("load %s: %w", name, err)
	}
	if scn.Name == "" {
		return nil, fmt.Errorf("load %s: name is required", name)
	}
	if scn.Input == "" {
		scn.Input = "text"
	}
	if scn.Input != "text" && scn.Input != "speech" {
		return nil, fmt.Errorf("load %s: input must be text or speech", name)
	}
	if scn.LatencyCondition != "" && scn.LatencyCondition != "baseline" && scn.LatencyCondition != "worker" {
		return nil, fmt.Errorf("load %s: invalid latency_condition", name)
	}
	if scn.Worker.SustainedLoad && scn.LatencyCondition == "" {
		return nil, fmt.Errorf("load %s: sustained worker load requires latency condition", name)
	}
	if scn.Capture.Microphone && (scn.Input != "speech" || !scn.Capture.Speakers) {
		return nil, fmt.Errorf("load %s: microphone capture requires speech input and capture.speakers", name)
	}
	for i := range scn.Turns {
		t := &scn.Turns[i]
		if scn.Capture.Microphone && t.WAV != "" && strings.TrimSpace(t.ExpectedTranscript) == "" {
			return nil, fmt.Errorf("load %s: microphone turn %q requires expected_transcript", name, t.ID)
		}
		if t.LatencyIteration < 0 || (t.LatencyIteration > 0 && (scn.LatencyCondition == "" || t.WAV == "" || scn.Input != "speech" || t.Op != "" || t.InterruptAfter.Duration != 0)) {
			return nil, fmt.Errorf("load %s: measured turn %q requires speech WAV, condition, and no control operation", name, t.ID)
		}
		if scn.Input == "speech" && t.User != "" && t.Op == "" && t.WAV == "" {
			return nil, fmt.Errorf("load %s: speech turn %q requires WAV", name, t.ID)
		}
		if t.WAV != "" && (scn.Input != "speech" || (t.Op != "" && t.Op != "acoustic_interrupt" && t.Op != "acoustic_completion" && t.Op != "acoustic_response" && t.Op != "acoustic_mute" && t.Op != "acoustic_failure") || t.InterruptAfter.Duration != 0) {
			return nil, fmt.Errorf("load %s: WAV turn requires speech input without control operation", name)
		}
		if t.InterruptAfter.Duration < 0 || (t.InterruptAfter.Duration > 0 && (t.User == "" || t.Op != "")) {
			return nil, fmt.Errorf("load %s: turn %q: interrupt_after requires a user turn and positive duration", name, t.ID)
		}
		if t.StimulusDelay != nil && (t.StimulusDelay.Duration < 0 || t.Op != "acoustic_interrupt") {
			return nil, fmt.Errorf("load %s: turn %q: stimulus_delay requires acoustic_interrupt and a nonnegative duration", name, t.ID)
		}
		if t.StimulusCompleteWithin != nil && (t.StimulusCompleteWithin.Duration <= 0 || t.Op != "acoustic_interrupt") {
			return nil, fmt.Errorf("load %s: turn %q: stimulus_complete_within requires acoustic_interrupt and a positive duration", name, t.ID)
		}
		if t.Op != "" && t.Op != "select_persona" && t.Op != "run_background" && t.Op != "quiet_gap" && t.Op != "compact" && t.Op != "start_latency_worker" && t.Op != "stop_latency_worker" && t.Op != "acoustic_interrupt" && t.Op != "acoustic_completion" && t.Op != "acoustic_response" && t.Op != "acoustic_mute" && t.Op != "acoustic_failure" {
			return nil, fmt.Errorf("load %s: turn %q: unknown op %q", name, t.ID, t.Op)
		}
		if t.Op == "acoustic_interrupt" && (!scn.Capture.Microphone || t.WAV == "" || t.InterruptWAV == "" || t.ExpectedTranscript == "") {
			return nil, fmt.Errorf("load %s: acoustic_interrupt needs physical capture, initial and interrupt WAVs, and expected transcript", name)
		}
		if t.Op == "acoustic_completion" && (!scn.Capture.Microphone || t.WAV == "" || t.ExpectedTranscript == "") {
			return nil, fmt.Errorf("load %s: acoustic_completion needs physical capture, fixture WAV, and expected transcript", name)
		}
		if (t.Op == "acoustic_mute" || t.Op == "acoustic_failure") && (!scn.Capture.Microphone || t.WAV == "" || strings.TrimSpace(t.ExpectedTranscript) == "" || t.User != "" || t.Duration.Duration != 0 || scn.LatencyCondition != "" || scn.TTS.Provider == "none") {
			return nil, fmt.Errorf("load %s: acoustic mute/failure needs physical speech fixture, expected transcript, real TTS and no latency/user/duration override", name)
		}
		if t.Op == "acoustic_response" && (!scn.Capture.Microphone || t.WAV == "" || t.ExpectedTranscript == "" || t.Duration.Duration <= 0) {
			return nil, fmt.Errorf("load %s: acoustic_response needs physical capture, fixture WAV, expected transcript, and positive next-listen duration", name)
		}
		if t.InterruptWAV != "" && t.Op != "acoustic_interrupt" {
			return nil, fmt.Errorf("load %s: interrupt_wav requires acoustic_interrupt", name)
		}
		if t.Op == "start_latency_worker" && (scn.LatencyCondition != "worker" || strings.TrimSpace(t.User) == "") {
			return nil, fmt.Errorf("load %s: start_latency_worker requires worker condition and explicit task", name)
		}
		if t.ExpectError != "" && t.Op != "select_persona" {
			return nil, fmt.Errorf("load %s: turn %q: expect_error requires op select_persona", name, t.ID)
		}
		if t.Wait == "" {
			t.Wait = waitIdle
		}
		switch t.Wait {
		case waitIdle, waitJobStarted, waitJobTerminal, waitWorkDurable:
		default:
			return nil, fmt.Errorf("load %s: turn %q: unknown wait %q", name, t.ID, t.Wait)
		}
		if t.User == "" && t.Op == "" && t.Wait != waitJobTerminal && t.Wait != waitIdle && t.Wait != waitWorkDurable {
			return nil, fmt.Errorf("load %s: turn %q: empty user only allowed with wait idle, job_terminal, or work_durable", name, t.ID)
		}
	}
	return &scn, nil
}

func loadScenarioString(name, body string) (*Scenario, error) {
	return loadScenarioReader(name, strings.NewReader(body))
}
