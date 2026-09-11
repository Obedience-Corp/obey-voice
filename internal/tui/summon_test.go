package tui

import (
	"context"
	"strings"
	"testing"

	"github.com/Obedience-Corp/obey-voice/internal/talkhotkey"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/pipeline"
)

const unaddressedUtterance = "what's for dinner?"

// The gate is the whole feature: speech that opens with the agent's name (or an
// alias) runs, and speech meant for the room is dropped with a reason that says
// who was not addressed and what was heard.
func TestNameSummonGateAnswersOnlyWhenAddressed(t *testing.T) {
	m := sizedConversation(t, 80, 24)
	m.agentName = "Obey"
	gate := m.addressGate(&config.Config{NameSummonAliases: []string{"computer"}})
	if gate == nil {
		t.Fatal("no gate built for a named persona")
	}

	for _, addressed := range []string{"Obey, what's the time?", "hey obey what's the time", "Computer, lights off"} {
		if reason := gate(addressed); reason != "" {
			t.Errorf("gate(%q) vetoed: %s", addressed, reason)
		}
	}

	reason := gate(unaddressedUtterance)
	if reason == "" {
		t.Fatal("gate let an utterance through that named nobody")
	}
	if !strings.Contains(reason, "Obey") {
		t.Errorf("veto reason does not name the agent: %q", reason)
	}
	if !strings.Contains(reason, unaddressedUtterance) {
		t.Errorf("veto reason does not quote what was heard: %q", reason)
	}
}

// A persona with no display name would produce a matcher that matches nothing,
// which as a gate means dropping every utterance for the whole session.
func TestNameSummonNeedsANameToAnswerTo(t *testing.T) {
	m := sizedConversation(t, 80, 24)
	m.agentName = "  "
	if gate := m.addressGate(&config.Config{}); gate != nil {
		t.Fatal("built a gate for a persona with no name")
	}
}

// The talk key is itself an address (D5): while it owns the turn the gate
// stands down, and it closes again once that turn is done.
func TestNameSummonTalkKeyBypassesTheGate(t *testing.T) {
	m, _ := talkConversation(t, &fakeTurnRunner{}, talkhotkey.ModeHold)
	m.agentName = "Obey"
	gate := m.addressGate(&config.Config{})
	if gate == nil {
		t.Fatal("no gate built")
	}
	if gate(unaddressedUtterance) == "" {
		t.Fatal("the gate was already open before the key was pressed")
	}

	m, _ = m.Update(keyDown())
	if reason := gate(unaddressedUtterance); reason != "" {
		t.Fatalf("the key did not bypass the gate: %s", reason)
	}

	m, _ = m.Update(voiceTurnDoneMsg{})
	if m.talkKeyTurn.Load() {
		t.Fatal("the key still owned the turn after it ended")
	}
	if gate(unaddressedUtterance) == "" {
		t.Fatal("the gate stayed open after the key's turn ended")
	}
}

func nameSummonRuntime(t *testing.T, cfg *config.Config) (*ConversationRuntime, *[]string) {
	t.Helper()
	rt := fakeRuntime()
	rt.AgentName = "Obey"
	rt.SessionCfg = cfg
	var infos []string
	events.Subscribe(rt.Bus, func(e events.Info) { infos = append(infos, e.Message) })
	return rt, &infos
}

func startSession(t *testing.T, rt *ConversationRuntime) App {
	t.Helper()
	app := wiredApp(func(context.Context, BuildProgress, string, string) (*ConversationRuntime, error) {
		return rt, nil
	})
	model, _ := app.Update(startPipelineMsg{personaName: rt.AgentName})
	app = model.(App)
	model, _ = app.Update(runtimeReadyMsg{rt: rt})
	return model.(App)
}

func activityDetail(m conversationModel, stage string) (string, bool) {
	for _, entry := range m.activity {
		if entry.stage == stage {
			return entry.detail, true
		}
	}
	return "", false
}

func TestNameSummonInstallsTheGateAndNamesTheWakeWords(t *testing.T) {
	rt, infos := nameSummonRuntime(t, &config.Config{
		NameSummonEnabled: true,
		NameSummonAliases: []string{"computer"},
		VoiceFrontend:     config.VoiceFrontendNLMS,
	})
	app := startSession(t, rt)

	if rt.Pipeline.AddressGate == nil {
		t.Fatal("name summon was on but no gate reached the pipeline")
	}
	if reason := rt.Pipeline.AddressGate(unaddressedUtterance); reason == "" {
		t.Fatal("the installed gate answers to anything")
	}
	detail, ok := activityDetail(app.conversation, "name summon")
	if !ok {
		t.Fatal("the activity panel does not say name summon is on")
	}
	if !strings.Contains(detail, "obey") || !strings.Contains(detail, "computer") {
		t.Errorf("activity detail = %q, want the wake words", detail)
	}
	if len(*infos) != 0 {
		t.Errorf("unexpected messages on a healthy session: %v", *infos)
	}
}

// D5 fails closed: with no front end the microphone hears playback, so a
// session that was asked for name summon refuses it and says so.
func TestNameSummonRefusedWithoutAVoiceFrontEnd(t *testing.T) {
	rt, infos := nameSummonRuntime(t, &config.Config{
		NameSummonEnabled: true,
		VoiceFrontend:     config.VoiceFrontendOff,
	})
	app := startSession(t, rt)

	if rt.Pipeline.AddressGate != nil {
		t.Fatal("the gate was installed with the voice front end off")
	}
	if _, ok := activityDetail(app.conversation, "name summon"); ok {
		t.Error("the activity panel claims name summon is on")
	}
	found := false
	for _, msg := range *infos {
		if msg == nameSummonOffMessage {
			found = true
		}
	}
	if !found {
		t.Fatalf("the session did not say why name summon is off: %v", *infos)
	}
}

// Off is the default, and the default must cost nothing: no gate, no message.
func TestNameSummonOffInstallsNothing(t *testing.T) {
	rt, infos := nameSummonRuntime(t, &config.Config{VoiceFrontend: config.VoiceFrontendNLMS})
	app := startSession(t, rt)

	if rt.Pipeline.AddressGate != nil {
		t.Fatal("a gate was installed while name summon is off")
	}
	if _, ok := activityDetail(app.conversation, "name summon"); ok {
		t.Error("the activity panel mentions a feature nobody turned on")
	}
	if len(*infos) != 0 {
		t.Errorf("unexpected messages: %v", *infos)
	}
}

func TestNameSummonSkipsLegacyGateWhenAdmissionOwnsAddressing(t *testing.T) {
	rt, infos := nameSummonRuntime(t, &config.Config{
		NameSummonEnabled: true,
		NameSummonAliases: []string{"computer"},
		VoiceFrontend:     config.VoiceFrontendNLMS,
	})
	rt.Pipeline.AdmitInput = func(context.Context, string, bool) (pipeline.ResponseAdmission, error) {
		return pipeline.ResponseAdmission{}, nil
	}
	app := startSession(t, rt)
	if rt.Pipeline.AddressGate != nil {
		t.Fatal("legacy address gate remained with shared admission routing")
	}
	if _, ok := activityDetail(app.conversation, "name summon"); ok {
		t.Fatal("legacy name-summon activity appeared for shared admission routing")
	}
	if len(*infos) != 0 {
		t.Fatalf("unexpected warning: %v", *infos)
	}
}

func TestSettingsToolsNameSummonNeedsTheFrontEnd(t *testing.T) {
	cfg := &config.Config{BrainProvider: "ollama", VoiceFrontend: config.VoiceFrontendOff}
	m := newSettings(cfg, nil)
	m.section = sectionTools
	if !strings.Contains(m.toolItems[toolRowNameSummon], "Name summon") || !strings.Contains(m.toolItems[toolRowNameSummon], "OFF") {
		t.Fatalf("name summon row = %q", m.toolItems[toolRowNameSummon])
	}

	saved := map[string]any{}
	m.saveConfig = func(key string, value any) error {
		saved[key] = value
		return nil
	}

	m.cursor = toolRowNameSummon
	m.selectCurrent()
	if cfg.NameSummonEnabled {
		t.Fatal("name summon was enabled with the voice front end off")
	}
	if len(saved) != 0 {
		t.Fatalf("a refused toggle still wrote config: %v", saved)
	}
	if !strings.Contains(m.message, "Voice frontend (AEC)") {
		t.Fatalf("the refusal does not say which setting to turn on: %q", m.message)
	}

	// With the front end on, the same row saves.
	cfg.VoiceFrontend = config.VoiceFrontendNLMS
	m.selectCurrent()
	if saved["name_summon_enabled"] != true || !cfg.NameSummonEnabled {
		t.Fatalf("enable failed: saved=%v live=%v", saved["name_summon_enabled"], cfg.NameSummonEnabled)
	}
	if !strings.Contains(m.toolItems[toolRowNameSummon], "ON") {
		t.Fatalf("name summon row after toggle = %q", m.toolItems[toolRowNameSummon])
	}

	// Turning it back off is never refused, whatever the front end is doing.
	cfg.VoiceFrontend = config.VoiceFrontendOff
	m.selectCurrent()
	if cfg.NameSummonEnabled || saved["name_summon_enabled"] != false {
		t.Fatalf("could not turn name summon back off: saved=%v live=%v", saved["name_summon_enabled"], cfg.NameSummonEnabled)
	}
}

// The AEC row writes the legacy bool, which only becomes a front end on the
// next load. Someone who just turned it on in this screen is not told no.
func TestSettingsToolsNameSummonAcceptsAJustEnabledFrontEnd(t *testing.T) {
	cfg := &config.Config{
		BrainProvider:        "ollama",
		VoiceFrontend:        config.VoiceFrontendOff,
		VoiceFrontendEnabled: true,
	}
	m := newSettings(cfg, nil)
	m.section = sectionTools
	m.saveConfig = func(string, any) error { return nil }
	m.cursor = toolRowNameSummon
	m.selectCurrent()
	if !cfg.NameSummonEnabled {
		t.Fatalf("name summon refused right after the AEC row was turned on: %q", m.message)
	}
}
