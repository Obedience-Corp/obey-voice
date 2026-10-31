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
	"sync"
	"sync/atomic"
	"testing"

	"github.com/Obedience-Corp/obey-voice/internal/persona"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/chatmedia"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/session"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/utterance"
)

func TestProductRestoreProjectionPreservesRecordingOrItsActualTranscript(t *testing.T) {
	for _, test := range []struct {
		name, mode    string
		ready, reject bool
	}{
		{"Standard rejects retained audio-only context", persona.AudioStandard, false, true},
		{"Standard uses ready transcript and typed instruction", persona.AudioStandard, true, false},
		{"Native restores exact retained WAV", persona.AudioNative, false, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := personaEnv(t)
			var calls, explicitASR atomic.Int32
			var mu sync.Mutex
			var observedImages [][]byte
			var observedText string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/api/version":
					fmt.Fprint(w, `{"version":"fixture"}`)
				case "/api/tags":
					fmt.Fprintf(w, `{"models":[{"name":"lfm3-audio:2b","digest":"%s"},{"name":"qwen2.5:14b"}]}`, strings.Repeat("a", 64))
				case "/api/show":
					fmt.Fprint(w, `{"capabilities":["completion","audio","speech"],"voices":["female"]}`)
				case "/v1/models":
					fmt.Fprint(w, `{"data":[{"id":"lfm3-audio:2b"}]}`)
				case "/v1/audio/voices":
					w.WriteHeader(404)
				case "/v1/audio/speech":
					w.Header().Set("Content-Type", "application/octet-stream")
					_, _ = w.Write([]byte{0, 0, 1, 0})
				case "/v1/audio/transcriptions":
					explicitASR.Add(1)
					fmt.Fprint(w, `{"text":"Recovered actual recording transcript"}`)
				case "/api/chat":
					var request struct {
						Messages []struct {
							Content string
							Images  [][]byte
						}
					}
					if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
						t.Error(err)
					}
					foreground := false
					for _, message := range request.Messages {
						if message.Content == "Continue from saved recording" {
							foreground = true
						}
					}
					if foreground {
						mu.Lock()
						for _, message := range request.Messages {
							observedImages = append(observedImages, message.Images...)
							observedText += message.Content + "\n"
						}
						mu.Unlock()
					}
					fmt.Fprint(w, `{"message":{"role":"assistant","content":"Fixture response."},"done":true,"done_reason":"stop"}`)
				default:
					t.Error("unexpected route", r.URL.Path)
					w.WriteHeader(404)
				}
			}))
			defer server.Close()
			data := fmt.Sprintf("active_persona: samantha\nbrain_provider: ollama\nollama_host: %s\nollama_model: qwen2.5:14b\nvoice_workers_disabled: true\nvoice_work_enabled: false\nvoice_tools_enabled: false\nskills_enabled: false\nvad_enabled: false\n", server.URL)
			if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(data), 0600); err != nil {
				t.Fatal(err)
			}
			p, err := persona.Load("samantha")
			if err != nil {
				t.Fatal(err)
			}
			p.Audio = &persona.Audio{Mode: test.mode, Endpoint: server.URL, Voice: "female", Controls: persona.AudioControlsConversation, Transcript: persona.AudioTranscriptOff, RetainAudio: true}
			writePersona(t, p)
			media := chatmedia.NewAudioSession()
			defer media.Close()
			ref, err := media.ImportSamples(t.Context(), []float32{0.1, 0.2, -0.2})
			if err != nil {
				t.Fatal(err)
			}
			wav, err := media.ResolveAudio(t.Context(), ref)
			if err != nil {
				t.Fatal(err)
			}
			saved := session.New("ollama", "lfm3-audio:2b")
			saved.SelectedPersonaID = "samantha"
			disk, err := session.DefaultStore().AudioMedia(saved.ID)
			if err != nil {
				t.Fatal(err)
			}
			if err := disk.PersistAudio(t.Context(), ref, wav); err != nil {
				t.Fatal(err)
			}
			input := &utterance.Finalized{ID: "retained-input", CaptureEpoch: 17, Audio: &ref, Transcript: utterance.Transcript{State: utterance.Disabled}}
			if test.ready {
				input.Transcript = utterance.Transcript{State: utterance.Ready, Text: "Actual recording transcript", Provider: "ollama", Model: "lfm3-audio:2b"}
			}
			if err := saved.Save([]brain.Turn{{Role: "user", Content: "Typed instruction", TypedInput: "Typed instruction", Utterance: input, AudioRetained: true}, {Role: "assistant", Content: "Prior response"}}); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, "sessions", saved.ID+".json")
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			rt, err := BuildDesktopConversation(t.Context(), DesktopOptions{SessionID: saved.ID, TextOnly: true, Silent: true}, nil)
			if test.reject {
				if rt != nil {
					rt.Cleanup()
				}
				if rt != nil || !errors.Is(err, brain.ErrAudioProjection) || calls.Load() != 0 {
					t.Fatal("Standard activated without a usable transcript", rt != nil, err, calls.Load())
				}
				after, readErr := os.ReadFile(path)
				if readErr != nil || !bytes.Equal(before, after) {
					t.Fatal("rejected projection modified saved evidence", readErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer rt.Cleanup()
			if err := rt.Pipeline.RunTurnTextMode(t.Context(), "Continue from saved recording"); err != nil {
				t.Fatal(err)
			}
			mu.Lock()

			if test.mode == persona.AudioNative {
				if len(observedImages) != 1 || !bytes.Equal(observedImages[0], wav) {
					t.Fatal("Native restore changed or omitted recording", len(observedImages))
				}
			} else if len(observedImages) != 0 || !strings.Contains(observedText, "Typed instruction") || !strings.Contains(observedText, "Actual recording transcript") {
				t.Fatal("Standard omitted the actual recording transcript", observedText)
			}
			mu.Unlock()
			if test.mode == persona.AudioNative {
				if explicitASR.Load() != 0 {
					t.Fatal("automatic transcription ran despite off")
				}
				p.Audio.Mode = persona.AudioStandard
				writePersona(t, p)
				if _, _, err := rt.SelectPersona(t.Context(), "samantha"); !errors.Is(err, brain.ErrAudioProjection) {
					t.Fatal("missing transcript did not preserve Native", err)
				}
				if rt.Pipeline.Native == nil {
					t.Fatal("failed selection retired Native")
				}
				if err := rt.Pipeline.TranscribeRecording(t.Context(), input.ID, input.CaptureEpoch); err != nil {
					t.Fatal(err)
				}
				if explicitASR.Load() != 1 {
					t.Fatal("explicit retry did not run exactly once", explicitASR.Load())
				}
				if _, _, err := rt.SelectPersona(t.Context(), "samantha"); err != nil {
					t.Fatal("explicit retry failed to repair projection", err)
				}
				rt.Persist()
				repaired, err := session.Load(saved.ID)
				if err != nil || repaired.Turns[0].Utterance.Transcript.Text != "Recovered actual recording transcript" {
					t.Fatal("retry not saved", err)
				}
			}
		})
	}
}
