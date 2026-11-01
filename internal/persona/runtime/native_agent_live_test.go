package personaruntime

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/internal/persona"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/chatmedia"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/nativeaudio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/pipeline"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/session"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/utterance"
)

// Explicit opt-in: synthetic files, installed selected Ollama model, actual
// ordinary voice pipeline and software drain. No microphone, audible device,
// download, HTTP proxy or prompt/transcript replacement is used.
func TestLiveNativeOrdinaryAgentAndHistory(t *testing.T) {
	fixtures := os.Getenv("OBEY_NATIVE_AGENT_FIXTURE_DIR")
	if fixtures == "" {
		t.Skip("set explicit synthetic fixture directory for local native agent proof")
	}
	output := os.Getenv("OBEY_NATIVE_AGENT_RECEIPT")
	if !filepath.IsAbs(fixtures) || !filepath.IsAbs(output) {
		t.Fatal("fixture and receipt paths must be explicit absolute paths")
	}
	revision, err := exec.Command("git", "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	status, err := exec.Command("git", "status", "--porcelain").Output()
	if err != nil || len(status) != 0 {
		t.Fatal("live proof requires exact committed source tree", err)
	}
	config.SetConfigDirForTest(t, t.TempDir())
	ctx, cancel := context.WithTimeout(t.Context(), 180*time.Second)
	defer cancel()
	receipt := map[string]any{"checkout_revision": string(bytes.TrimSpace(revision)), "scope": "ordinary native fixture host; production persona/history adapters", "sink": "software_drain", "devices": "none", "downloads": false, "proxy_or_prompt_replacement": false, "answer_quality_qualified": false, "profile_activation_ui_qualified": false}
	defer func() {
		receipt["test_pass"] = !t.Failed()
		data, err := json.MarshalIndent(receipt, "", "  ")
		if err != nil {
			t.Error(err)
			return
		}
		f, err := os.OpenFile(output, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			t.Error(err)
			return
		}
		if _, err := f.Write(append(data, '\n')); err != nil {
			t.Error(err)
		}
		if err := f.Close(); err != nil {
			t.Error(err)
		}
	}()
	base := &config.Config{BrainProvider: "ollama", OllamaHost: "http://localhost:11434", OllamaModel: "uninstalled-standard-worker", STTProvider: "must-not-construct-standard-recognition", TTSProvider: "kokoro", TTSFallbackProvider: "kokoro", ModelsDir: t.TempDir(), AgentName: "Samantha", WorkDir: t.TempDir(), RuntimeEnvUser: "fixture-user", RuntimeEnvHostname: "fixture-host", RuntimeEnvOS: "darwin", OllamaNumCtx: 8192, MaxHistory: 8, SkillsEnabled: false}
	unchanged := base.Clone()
	profile := &persona.Profile{Schema: persona.Schema, ID: "native-fixture", DisplayName: "Samantha", Prompts: persona.PromptRefs{Persona: "samantha"}, Brain: persona.Brain{Provider: "ollama", Model: "uninstalled-standard-worker"}, TTS: persona.TTS{Provider: "kokoro", Voice: "af_heart"}, Audio: &persona.Audio{Mode: persona.AudioNative, Model: "lfm3-audio:2b", Endpoint: "http://localhost:11434", Voice: "female", Controls: persona.AudioControlsConversation, Transcript: persona.AudioTranscriptOff, RetainAudio: true}}
	if err := persona.Write(profile, false); err != nil {
		t.Fatal(err)
	}
	catalog := persona.Catalog{Dir: persona.Dir()}
	binding, _, err := catalog.ResolveCandidateBindingWithIssues(base, profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	cfg := binding.Config()
	store := session.NewStore(filepath.Join(config.ConfigDir(), "sessions"))
	saved := session.New("ollama", cfg.OllamaModel)
	disk, err := store.AudioMedia(saved.ID)
	if err != nil {
		t.Fatal(err)
	}
	media := chatmedia.NewAudioSession()
	defer media.Close()
	cache := nativeaudio.NewSupportCache()
	mode := binding.AudioConfig()
	candidate, err := voiceagent.PrepareNativeCandidate(ctx, cfg, voiceagent.NativeCandidateOptions{Lifetime: ctx, Media: media, Store: &disk, Cache: cache, Controls: mode.Controls, Transcript: mode.Transcript, RetainAudio: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = candidate.Close() }()
	receipt["setup"] = candidate.Setup
	observer := &liveRequestObserver{base: candidate.Policy.Client.HTTPClient().Transport}
	candidate.Policy.Client.HTTPClient().Transport = observer
	receipt["observer_scope"] = "after quiet setup; actual ordinary-agent requests; no rewriting"
	input := &liveNativeInput{event: utterance.Event{Kind: utterance.Timeout}}
	sink := &liveSoftwareSink{}
	bus := events.NewBus()
	var eventMu sync.Mutex
	timeline := []string{}
	events.Subscribe(bus, func(events.ThinkingComplete) {
		eventMu.Lock()
		timeline = append(timeline, "chat_completed")
		eventMu.Unlock()
	})
	events.Subscribe(bus, func(events.SpeakingStarted) {
		eventMu.Lock()
		timeline = append(timeline, "speech_pcm_ready_software_sink")
		eventMu.Unlock()
	})
	stages := []string{}
	voices := &voiceagent.LiveTTSManager{RetireDelay: -1}
	defer voices.Close()
	authorize := func(context.Context, *voiceagent.NativeCandidate) error { return nil } // fixture-only integration scope
	agent, cleanup, err := candidate.NewAgent(ctx, voiceagent.Options{Events: bus, Input: input, Capture: &liveNativeMonitor{}, Player: sink, Worker: newSelectorTestBrain("unused worker", nil), LiveTTS: voices, Stage: func(stage string) { stages = append(stages, stage) }}, authorize)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if err := agent.BeginConversationIdentity(saved.ID); err != nil {
		t.Fatal(err)
	}
	if err := candidate.BindScope(agent.ConversationScope()); err != nil {
		t.Fatal(err)
	}
	hooks := &NativeSelectionHooks{TextOnly: false,
		Prepare: func(ctx context.Context, cfg *config.Config, mode persona.Audio, _ events.PersonaSelection, publish func(context.Context, func() error) error) (*voiceagent.NativeCandidate, error) {
			return voiceagent.PrepareNativeCandidate(ctx, cfg, voiceagent.NativeCandidateOptions{Lifetime: ctx, Media: media, Store: &disk, Cache: cache, Controls: mode.Controls, Transcript: mode.Transcript, RetainAudio: mode.RetainAudio, PublishCaption: publish})
		},
		Authorize: func(context.Context, *persona.SessionBinding, *voiceagent.NativeCandidate) error { return nil },
		PrepareInput: func(context.Context, *config.Config, *voiceagent.NativeCandidate) (pipeline.InputBinding, func(), error) {
			return pipeline.InputBinding{Input: input, Native: candidate.Policy}, nil, nil
		},
	}
	selector, err := NewSelector(SelectionOptions{Base: base, Catalog: catalog, Pipeline: agent.Pipeline, Voices: voices, SessionID: saved.ID, Initial: binding, InitialNative: candidate, Native: hooks, Persist: func(turns []brain.Turn, selected events.PersonaSelection) error {
		return store.SaveSelectionState(saved, turns, selected)
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer selector.Close()
	agent.AdmitAudio = func(ctx context.Context, _ *utterance.Finalized) (pipeline.ResponseAdmission, error) {
		envelope, err := selector.Admit(ctx)
		return pipeline.ResponseAdmission{Envelope: envelope}, err
	}
	agent.AdmitInput = selector.Route
	var saveErrors []error
	agent.OnTurn = func() {
		turns := agent.Brain.History()
		if canonical, ok := agent.Brain.(brain.CanonicalHistoryProvider); ok {
			turns = canonical.CanonicalHistory()
		}
		if err := store.Save(saved, turns); err != nil {
			saveErrors = append(saveErrors, err)
		}
	}
	if _, err := agent.RunTurn(ctx); err != nil {
		t.Fatal(err)
	}
	if len(observer.snapshot()) != 0 || len(agent.Brain.History()) != 0 {
		t.Fatal("no-speech fixture dispatched or admitted content")
	}
	receipt["no_speech_http_requests"] = 0
	results := []map[string]any{}
	answers := []string{}
	expectedAudio := []string{}
	for i, fixture := range []struct{ name, expected string }{{"addition", "15"}, {"multiplication", "12"}} {
		data, err := os.ReadFile(filepath.Join(fixtures, fixture.name+".wav"))
		if err != nil {
			t.Fatal(err)
		}
		sourceSum := sha256.Sum256(data)
		ref, err := media.Import(ctx, data)
		if err != nil {
			t.Fatal(err)
		}
		started, ended := nativeFixtureTimes(ref.SampleCount)
		input.event = utterance.Event{Kind: utterance.FinalizedEvent, Input: &utterance.Finalized{ID: "fixture-" + fixture.name, CaptureEpoch: uint64(i + 1), Audio: &ref, Transcript: utterance.Transcript{State: utterance.Pending}, Spans: nativeFixtureSpan(ref.SampleCount), StartedAt: started, EndedAt: ended, EndpointReason: "fixture eof"}}
		pcmBefore := len(sink.snapshot())
		if _, err := agent.RunTurn(ctx); err != nil {
			t.Fatal(err)
		}
		samples := 0
		for _, segment := range sink.snapshot()[pcmBefore:] {
			samples += segment["samples"].(int)
		}
		if samples == 0 {
			t.Fatal("actual reply produced no software-drained PCM")
		}
		expectedAudio = append(expectedAudio, ref.ID)
		turns := agent.Brain.History()
		answer := ""
		for _, turn := range turns {
			if turn.Role == "assistant" {
				answer = turn.Content
			}
		}
		if answer == "" {
			t.Fatal("actual native model produced no response")
		}
		answers = append(answers, answer)
		if _, err := disk.ReadAudio(ctx, ref); err != nil {
			t.Fatal("canonical retained audio not published before autosave", err)
		}
		results = append(results, map[string]any{"fixture": fixture.name, "source_sha256": hex.EncodeToString(sourceSum[:]), "audio_ref": ref, "expected_answer": fixture.expected, "actual_response": answer, "caption_state": "disabled", "retained_audio_verified": true, "reply_pcm_samples": samples})
	}
	chatIndex, speechRequests, asrRequests := 0, 0, 0
	for _, request := range observer.snapshot() {
		switch request["route"] {
		case "/api/chat":
			if request["model"] != cfg.OllamaModel || request["stream"] != true {
				t.Fatal("ordinary chat was substituted or unobserved", request["route"])
			}
			users := 0
			for _, message := range request["messages"].([]map[string]any) {
				if message["role"] != "user" {
					continue
				}
				if message["user_text"] != "" {
					t.Fatal("transcript leaked into audio-only wire input")
				}
				images := message["audio_wire"].([]map[string]any)
				if len(images) != 1 || users >= len(expectedAudio) || images[0]["sha256"] != expectedAudio[users] || images[0]["riff_wave"] != true {
					t.Fatal("actual audio/history wire did not match admitted WAV")
				}
				users++
			}
			if users != chatIndex+1 {
				t.Fatal("production history dropped an earlier recording", users, chatIndex)
			}
			chatIndex++
		case "/v1/audio/speech":
			speechRequests++
			if request["model"] != cfg.OllamaModel {
				t.Fatal("speech did not use selected LFM3 model")
			}
		case "/v1/audio/transcriptions":
			asrRequests++
		}
	}
	if chatIndex != 2 || speechRequests < 2 || asrRequests != 0 {
		t.Fatal("ordinary native request counts", chatIndex, speechRequests, asrRequests)
	}
	receipt["actual_chat_requests"], receipt["actual_speech_requests"], receipt["actual_asr_requests"] = chatIndex, speechRequests, asrRequests
	receipt["persona_prompt_ref"], receipt["persona_prompt_hash"] = cfg.Persona, selector.Snapshot().PromptHash
	receipt["turns"], receipt["requests"], receipt["pcm_segments"], receipt["construction_stages"] = results, observer.snapshot(), sink.snapshot(), stages
	receipt["input_sessions"] = input.starts.Load()
	receipt["maximum_active_inference_bodies"] = observer.max.Load()
	eventMu.Lock()
	receipt["timeline"] = append([]string(nil), timeline...)
	eventMu.Unlock()
	if len(saveErrors) > 0 {
		t.Fatal(saveErrors)
	}
	if len(stages) != 0 || agent.STT != nil {
		t.Fatal("ordinary native constructor built Standard providers", stages)
	}
	if observer.max.Load() != 1 {
		t.Fatal("native inference requests overlapped")
	}
	if answers[0] == answers[1] {
		t.Fatal("distinct actual recordings returned identical responses; preserve this failed trial")
	}
	modelDir, err := os.ReadDir(cfg.ModelsDir)
	if err != nil || len(modelDir) != 0 {
		t.Fatal("native ordinary startup populated external model store", err)
	}
	if !reflect.DeepEqual(base, unchanged) {
		t.Fatal("native startup mutated global Standard settings")
	}
	reloaded, err := store.Load(saved.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Selection == nil || reloaded.Selection.AudioMode != "native" || reloaded.FormatVersion != 3 {
		t.Fatal("saved effective native mode missing")
	}
	userTurns := 0
	for _, turn := range reloaded.Turns {
		if turn.Role == "user" {
			userTurns++
			if turn.Content != "" || turn.Utterance == nil || turn.Utterance.Transcript.State != utterance.Disabled || !turn.AudioRetained {
				t.Fatal("canonical audio-only turn was relabeled or lost")
			}
		}
	}
	if userTurns != 2 || len(sink.snapshot()) == 0 {
		t.Fatal("ordinary native pipeline did not retain two inputs and synthesize output")
	}
	receipt["saved_mode"], receipt["format_version"], receipt["canonical_audio_only_turns"] = reloaded.Selection.AudioMode, reloaded.FormatVersion, userTurns
	receipt["external_model_store_entries"], receipt["global_standard_settings_unchanged"] = len(modelDir), true
	receipt["injected_worker"] = "unused fixture worker; independent worker configuration preserved, no worker runtime claim"
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		t.Fatal(ctx.Err())
	}
}
