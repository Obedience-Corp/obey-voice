//go:build ignore

// Explicit fixture generator; never built into the application.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"os"
	"path/filepath"

	"github.com/Obedience-Corp/obey-voice/internal/persona"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/chatmedia"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/session"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/utterance"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/wav"
)

type scope struct{}

func (scope) Helper()        {}
func (scope) Cleanup(func()) {}
func main() {
	root := os.Getenv("OBEY_VOICE_TEST_CONFIG")
	if !filepath.IsAbs(root) {
		panic("absolute private fixture required")
	}
	config.SetConfigDirForTest(scope{}, root)
	cfg, err := config.LoadRaw()
	must(err)
	must(persona.EnsureAndApply(cfg))
	p, err := persona.Load("samantha")
	must(err)
	p.Audio = &persona.Audio{Mode: persona.AudioNative, Endpoint: cfg.OllamaHost, Voice: "female", Controls: persona.AudioControlsConversation, Transcript: persona.AudioTranscriptOff, RetainAudio: true}
	must(persona.Write(p, false))
	media := chatmedia.NewAudioSession()
	defer media.Close()
	samples := make([]float32, 16000*3)
	for i := range samples {
		samples[i] = float32(.1 * math.Sin(2*math.Pi*440*float64(i)/16000))
	}
	ref, err := media.ImportSamples(context.Background(), samples)
	must(err)
	data, err := media.ResolveAudio(context.Background(), ref)
	must(err)
	saved := session.New("ollama", "lfm3-audio:2b")
	saved.SelectedPersonaID = "samantha"
	disk, err := session.DefaultStore().AudioMedia(saved.ID)
	must(err)
	must(disk.PersistAudio(context.Background(), ref, data))
	input := &utterance.Finalized{ID: "recorded-fixture", CaptureEpoch: 17, Audio: &ref, Transcript: utterance.Transcript{State: utterance.Disabled}}
	must(saved.Save([]brain.Turn{{Role: "user", Utterance: input, AudioRetained: true}, {Role: "assistant", Content: "Prior fixture reply."}}))
	f, err := wav.ReadWAVFormat(bytes.NewReader(data), int64(len(data)))
	must(err)
	hash := sha256.Sum256(data[f.DataOffset : f.DataOffset+f.DataBytes])
	receipt, err := json.Marshal(map[string]any{"session_id": saved.ID, "id": input.ID, "epoch": input.CaptureEpoch, "samples": ref.SampleCount, "sample_rate": 16000, "pcm_sha256": hex.EncodeToString(hash[:])})
	must(err)
	must(os.WriteFile(filepath.Join(root, "recording-fixture.json"), receipt, 0600))
}
func must(err error) {
	if err != nil {
		panic(err)
	}
}
