//go:build !integration

package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Obedience-Corp/obey-voice/internal/persona"
	appTUI "github.com/Obedience-Corp/obey-voice/internal/tui"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/chatmedia"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/session"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/utterance"
)

func TestNativeProductHostStartsSwitchesAndRestartsWithoutStandardAssets(t *testing.T) {
	dir := personaEnv(t)
	var speech, chat, asr atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/version":
			fmt.Fprint(w, `{"version":"fixture"}`)
		case "/api/tags":
			fmt.Fprintf(w, `{"models":[{"name":"fixture-model","digest":"%s"},{"name":"qwen2.5:14b"}]}`, strings.Repeat("a", 64))
		case "/api/show":
			fmt.Fprint(w, `{"capabilities":["completion","audio","speech"],"voices":["female"]}`)
		case "/v1/models":
			fmt.Fprint(w, `{"data":[{"id":"fixture-model"}]}`)
		case "/v1/audio/voices":
			w.WriteHeader(404)
		case "/v1/audio/speech":
			speech.Add(1)
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = w.Write([]byte{0, 0, 1, 0})
		case "/v1/audio/transcriptions":
			asr.Add(1)
			t.Error("Conversation/off started ASR")
			w.WriteHeader(404)
		case "/api/chat":
			chat.Add(1)
			fmt.Fprintln(w, `{"model":"fixture-model","message":{"role":"assistant","content":"Fixture answer."},"done":true}`)
		default:
			t.Error("unexpected route", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	data := fmt.Sprintf("active_persona: samantha\nbrain_provider: ollama\nollama_host: %s\nollama_model: fixture-model\ntts_provider: kokoro\ntts_voice: af_heart\nmodels_dir: %s\nvoice_workers_disabled: true\nvoice_work_enabled: false\nvoice_tools_enabled: false\nskills_enabled: false\nvad_enabled: false\n", server.URL, filepath.Join(dir, "empty-models"))
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	p, err := persona.Load("samantha")
	if err != nil {
		t.Fatal(err)
	}
	p.Audio = &persona.Audio{Mode: persona.AudioNative, Model: "fixture-model", Endpoint: server.URL, Voice: "female", Controls: persona.AudioControlsConversation, Transcript: persona.AudioTranscriptOff}
	writePersona(t, p)
	global, err := os.ReadFile(filepath.Join(dir, "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	rt, err := BuildDesktopConversation(t.Context(), DesktopOptions{PersonaID: "samantha", TextOnly: true, Silent: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	live := true
	defer func() {
		if live {
			rt.Cleanup()
		}
	}()
	if rt.Pipeline.Native == nil || rt.Pipeline.STT != nil || rt.Pipeline.Input != nil || rt.Pipeline.Player != nil || rt.Pipeline.HasTTS() || !rt.Pipeline.OutputMuted() {
		t.Fatal("native text/silent host constructed the wrong stack")
	}
	if err := rt.Pipeline.RunTurnTextMode(t.Context(), "A typed fixture question"); err != nil {
		t.Fatal(err)
	}
	if asr.Load() != 0 || speech.Load() != 1 {
		t.Fatal("silent foreground inferred ASR/speech", asr.Load(), speech.Load())
	}
	p.Audio.Mode = persona.AudioStandard
	writePersona(t, p)
	if _, cfg, err := rt.SelectPersona(t.Context(), "samantha"); err != nil || cfg.TTSProvider != "kokoro" || rt.Pipeline.Native != nil {
		t.Fatal("Standard switch did not restore saved stack", err)
	}
	p.Audio.Mode = persona.AudioNative
	writePersona(t, p)
	if _, _, err := rt.SelectPersona(t.Context(), "samantha"); err != nil {
		t.Fatal(err)
	}
	if rt.Pipeline.HasTTS() {
		t.Fatal("silent replacement installed a speech provider")
	}
	rt.Persist()
	saved, err := session.Load(rt.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Selection == nil || saved.Selection.AudioMode != persona.AudioNative || saved.FormatVersion != 3 || len(saved.Turns) != 2 {
		encoded, _ := json.Marshal(saved)
		t.Fatalf("native selection/history not persisted: %s", encoded)
	}
	id := rt.SessionID
	rt.Cleanup()
	live = false
	restored, err := BuildDesktopConversation(t.Context(), DesktopOptions{SessionID: id, TextOnly: true, Silent: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Cleanup()
	if err := restored.Pipeline.ClearConversation(); err != nil {
		t.Fatal(err)
	}
	if err := restored.Pipeline.BeforeCapture(t.Context()); err != nil {
		t.Fatal("clear left capture on retired scope", err)
	}
	if restored.SelectionSnapshot().ConversationEpoch != restored.Pipeline.ConversationScope().ConversationEpoch {
		t.Fatal("next capture would use old context epoch")
	}
	if restored.Pipeline.Native == nil || len(restored.Seed) != 2 || restored.SessionID != id {
		t.Fatal("restart did not restore native mode/history")
	}
	after, err := os.ReadFile(filepath.Join(dir, "config.yaml"))
	if err != nil || !bytes.Equal(global, after) {
		t.Fatal("conversation changed global Standard choices", err)
	}
	entries, err := os.ReadDir(filepath.Join(dir, "empty-models"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatal("native host populated external model store")
	}
	if chat.Load() < 4 {
		t.Fatal("fixture did not traverse setup/foreground/restart")
	}
}

func TestNativeProductUnavailableSetupKeepsDraftAndCreatesNoSession(t *testing.T) {
	dir := personaEnv(t)
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	p, err := persona.Load("samantha")
	if err != nil {
		t.Fatal(err)
	}
	p.Audio = &persona.Audio{Mode: persona.AudioNative, Endpoint: server.URL, Controls: persona.AudioControlsConversation, Transcript: persona.AudioTranscriptOff}
	writePersona(t, p)
	before, err := os.ReadFile(p.Path)
	if err != nil {
		t.Fatal(err)
	}
	rt, err := BuildDesktopConversation(t.Context(), DesktopOptions{PersonaID: "samantha", TextOnly: true, Silent: true}, nil)
	var unavailable *appTUI.PersonaBindingError
	if err == nil || rt != nil || !errors.As(err, &unavailable) || !strings.Contains(err.Error(), "retry setup or select Standard") {
		t.Fatal("missing recoverable unavailable startup", rt, err)
	}
	if unavailable.SessionID != "" {
		t.Fatal("failed new startup advertised nonexistent saved session")
	}
	if requests.Load() != 1 {
		t.Fatal("failed setup started extra inference", requests.Load())
	}
	after, err := os.ReadFile(p.Path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("failed setup modified native draft", err)
	}
	paths, err := filepath.Glob(filepath.Join(dir, "sessions", "*.json"))
	if err != nil || len(paths) != 0 {
		t.Fatal("failed setup published a session", paths, err)
	}
}

func TestNativeProductMissingAudioContextRejectsBeforeInferenceWithoutChangingSession(t *testing.T) {
	dir := personaEnv(t)
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		t.Error("missing audio context reached runtime")
		w.WriteHeader(500)
	}))
	defer server.Close()
	p, err := persona.Load("samantha")
	if err != nil {
		t.Fatal(err)
	}
	p.Audio = &persona.Audio{Mode: persona.AudioNative, Endpoint: server.URL, Controls: persona.AudioControlsConversation, Transcript: persona.AudioTranscriptOff}
	writePersona(t, p)
	media := chatmedia.NewAudioSession()
	defer media.Close()
	ref, err := media.ImportSamples(t.Context(), []float32{0.1, 0.2})
	if err != nil {
		t.Fatal(err)
	}
	saved := session.New("ollama", "lfm3-audio:2b")
	saved.SelectedPersonaID = "samantha"
	input := &utterance.Finalized{ID: "missing-recording", Audio: &ref, Transcript: utterance.Transcript{State: utterance.Disabled}}
	if err := saved.Save([]brain.Turn{{Role: "user", Content: "Typed instruction only", TypedInput: "Typed instruction only", Utterance: input}}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "sessions", saved.ID+".json")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	rt, err := BuildDesktopConversation(t.Context(), DesktopOptions{SessionID: saved.ID, TextOnly: true, Silent: true}, nil)
	if rt != nil || !errors.Is(err, brain.ErrAudioProjection) {
		t.Fatal("missing recording silently omitted", rt, err)
	}
	if requests.Load() != 0 {
		t.Fatal("unavailable context started inference")
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("failed restore modified original saved evidence", err)
	}
}
