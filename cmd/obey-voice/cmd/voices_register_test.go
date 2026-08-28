//go:build !integration

package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/tts"
)

// installTestAudio8Export writes the two files Audio8ExportInstalled looks for.
// Registration is faked past that point: encoding needs 400 MB of weights, and
// the flag contract, the consent gate and the store layout are what this file
// is about.
func installTestAudio8Export(t *testing.T, modelsDir string) {
	t.Helper()
	dir := filepath.Join(modelsDir, config.Audio8Subdir)
	if err := os.MkdirAll(filepath.Join(dir, "tokenizer"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{config.Audio8TokenizerPath(dir), filepath.Join(dir, "runtime_manifest.json")} {
		if err := os.WriteFile(path, []byte(`{"slow_logits_layout":"relative_semantic_then_eos"}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// The registration manifest carries max_audio_bytes, which the pre-decode
	// size gate reads before anything opens the WAV.
	reg := filepath.Join(dir, "registration")
	if err := os.MkdirAll(reg, 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"model_fingerprint":"audio8-tts-preview-0.1b-int8-v1","sample_rate":44100,` +
		`"num_codebooks":10,"encoder":"codec_encoder_fp16.onnx","max_audio_bytes":52428800}`
	if err := os.WriteFile(filepath.Join(reg, "registration_manifest.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// newAudio8TestConfig points a throwaway config at a throwaway models dir.
func newAudio8TestConfig(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	modelsDir := filepath.Join(dir, "models")
	if err := os.MkdirAll(modelsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	config.SetConfigDirForTest(t, dir)
	if err := config.SetAndSave("models_dir", modelsDir); err != nil {
		t.Fatal(err)
	}
	return modelsDir
}

// fakeRegistration swaps the encoder and Kokoro for recorders, and returns the
// request the command built.
func fakeRegistration(t *testing.T) *tts.Audio8RegisterRequest {
	t.Helper()
	var seen tts.Audio8RegisterRequest
	orig := voiceRegister
	t.Cleanup(func() { voiceRegister = orig })
	voiceRegister = voiceRegisterDeps{
		register: func(_ context.Context, req tts.Audio8RegisterRequest) (tts.Audio8VoiceMeta, error) {
			seen = req
			meta := tts.Audio8VoiceMeta{
				Name: req.Name, Transcript: req.Transcript, Locale: req.Locale,
				ModelFingerprint: "audio8-tts-preview-0.1b-int8-v1",
				NumCodebooks:     10, Frames: 108, SampleRate: 44100,
				Provenance: req.Provenance,
			}
			// The real path writes before it reports; the fake does too, so the
			// store layout is exercised.
			codes := make([][]int64, 10)
			for i := range codes {
				codes[i] = make([]int64, 108)
			}
			if err := tts.SaveAudio8Voice(req.ModelsDir, meta, codes); err != nil {
				return tts.Audio8VoiceMeta{}, err
			}
			return tts.LoadAudio8VoiceMeta(req.ModelsDir, req.Name)
		},
		readWAV: func(string) ([]float32, int, error) { return make([]float32, 44100*5), 44100, nil },
		digest:  func(string) (string, error) { return "deadbeef", nil },
		stat:    func(string) (os.FileInfo, error) { return fakeFileInfo{size: 1 << 20}, nil },
		synthesize: func(_ context.Context, _ *config.Config, voice, text string) ([]float32, int, error) {
			if voice == "" || text == "" {
				t.Errorf("bootstrap called with voice %q text %q", voice, text)
			}
			synthesized = voice
			return make([]float32, 24000*10), 24000, nil
		},
	}
	return &seen
}

// synthesized records the voice the bootstrap actually handed to Kokoro, which
// is the thing the stored provenance has to agree with.
var synthesized string

// fakeFileInfo is a regular file of a chosen size, for the pre-decode size gate.
type fakeFileInfo struct {
	size int64
	mode fs.FileMode
}

func (f fakeFileInfo) Name() string       { return "ref.wav" }
func (f fakeFileInfo) Size() int64        { return f.size }
func (f fakeFileInfo) Mode() fs.FileMode  { return f.mode }
func (f fakeFileInfo) ModTime() time.Time { return time.Time{} }
func (f fakeFileInfo) IsDir() bool        { return f.mode.IsDir() }
func (f fakeFileInfo) Sys() any           { return nil }

func runVoicesSub(t *testing.T, args ...string) (string, error) {
	t.Helper()
	root := &cobra.Command{Use: "obey-voice"}
	root.AddCommand(newVoicesRegisterCmd(), newVoicesRegisteredCmd(), newVoicesRemoveCmd())
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs(args)
	err := root.Execute()
	return out.String(), err
}

func TestVoicesRegisterRefusesBadInvocations(t *testing.T) {
	tests := []struct {
		name    string
		install bool
		args    []string
		want    string
	}{
		{
			name:    "no reference audio at all",
			install: true,
			args:    []string{"register", "mine"},
			want:    "--wav FILE with --text, or --from-kokoro VOICE",
		},
		{
			name:    "both sources",
			install: true,
			args:    []string{"register", "mine", "--wav", "a.wav", "--from-kokoro", "af_heart"},
			want:    "alternatives",
		},
		{
			name:    "a recording with no transcript",
			install: true,
			args:    []string{"register", "mine", "--wav", "a.wav", "--consent"},
			want:    "--text is required",
		},
		{
			name:    "a recording with no consent",
			install: true,
			args:    []string{"register", "mine", "--wav", "a.wav", "--text", "hello there"},
			want:    "--consent",
		},
		{
			name:    "the packaged voice's name is reserved",
			install: true,
			args:    []string{"register", config.Audio8PackagedVoice, "--from-kokoro", "af_heart"},
			want:    "reserved",
		},
		{
			name:    "a name that would escape the store",
			install: true,
			args:    []string{"register", "../escape", "--from-kokoro", "af_heart"},
			want:    "invalid voice name",
		},
		{
			name:    "no export installed",
			install: false,
			args:    []string{"register", "mine", "--from-kokoro", "af_heart"},
			want:    "no Audio8 export",
		},
		{
			// Kokoro resolves its voice by exact map key and silently falls
			// back to af_heart, so a name it cannot resolve must be refused
			// here rather than recorded as the voice that spoke.
			name:    "an unknown kokoro voice",
			install: true,
			args:    []string{"register", "mine", "--from-kokoro", "af_nonesuch"},
			want:    "is not a Kokoro voice",
		},
		{
			name:    "consent is meaningless for synthesized audio",
			install: true,
			args:    []string{"register", "mine", "--from-kokoro", "af_heart", "--consent"},
			want:    "--consent does not apply",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			modelsDir := newAudio8TestConfig(t)
			if tt.install {
				installTestAudio8Export(t, modelsDir)
			}
			fakeRegistration(t)
			_, err := runVoicesSub(t, tt.args...)
			if err == nil {
				t.Fatalf("register error = nil, want %q", tt.want)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("register error = %q, want it to mention %q", err, tt.want)
			}
			if names := config.Audio8RegisteredVoiceNames(modelsDir); len(names) != 0 {
				t.Errorf("a refused registration wrote %v", names)
			}
		})
	}
}

func TestVoicesRegisterFromAWAV(t *testing.T) {
	modelsDir := newAudio8TestConfig(t)
	installTestAudio8Export(t, modelsDir)
	seen := fakeRegistration(t)

	out, err := runVoicesSub(t, "register", "mine", "--wav", filepath.Join("ref", "take2.wav"),
		"--text", "  exactly   what is said  ", "--locale", "en-GB", "--consent", "--json")
	if err != nil {
		t.Fatalf("register: %v (%s)", err, out)
	}
	if seen.Provenance.Kind != tts.Audio8VoiceRecorded {
		t.Errorf("provenance kind = %q, want %q", seen.Provenance.Kind, tts.Audio8VoiceRecorded)
	}
	if !seen.Provenance.Consent {
		t.Error("consent was not recorded on a --consent registration")
	}
	if seen.Provenance.SourceFile != "take2.wav" {
		t.Errorf("source_file = %q, want the base name only (the store is copyable between machines)", seen.Provenance.SourceFile)
	}
	if seen.Provenance.SourceSHA256 != "deadbeef" {
		t.Errorf("source_sha256 = %q, want the digest of the WAV", seen.Provenance.SourceSHA256)
	}
	if seen.SampleRate != 44100 || len(seen.Samples) == 0 {
		t.Errorf("samples = %d at %d Hz, want the WAV's", len(seen.Samples), seen.SampleRate)
	}

	var row audio8VoiceJSON
	if err := json.Unmarshal([]byte(out), &row); err != nil {
		t.Fatalf("parse --json: %v (%s)", err, out)
	}
	if row.Name != "mine" || row.Locale != "en-GB" || !row.Consent {
		t.Errorf("json row = %+v", row)
	}
	if row.Transcript != "exactly what is said" {
		t.Errorf("transcript = %q, want the whitespace collapsed", row.Transcript)
	}
}

// The bootstrap is the consent-clean path: no recording, no consent flag, and
// the record says the audio was synthesized here.
func TestVoicesRegisterFromKokoro(t *testing.T) {
	modelsDir := newAudio8TestConfig(t)
	installTestAudio8Export(t, modelsDir)
	seen := fakeRegistration(t)

	out, err := runVoicesSub(t, "register", "english", "--from-kokoro", "af_heart", "--json")
	if err != nil {
		t.Fatalf("register: %v (%s)", err, out)
	}
	if seen.Provenance.Kind != tts.Audio8VoiceSynthetic {
		t.Errorf("provenance kind = %q, want %q", seen.Provenance.Kind, tts.Audio8VoiceSynthetic)
	}
	if seen.Provenance.Consent {
		t.Error("synthetic audio recorded consent; there is nobody to have given it")
	}
	if seen.Provenance.BootstrapVoice != "af_heart" || seen.Provenance.BootstrapProvider != "kokoro" {
		t.Errorf("bootstrap provenance = %+v", seen.Provenance)
	}
	if seen.Transcript != audio8BootstrapText {
		t.Errorf("transcript = %q, want the default bootstrap line", seen.Transcript)
	}
	if seen.Locale != tts.VoiceInfo("af_heart").Locale {
		t.Errorf("locale = %q, want Kokoro's own locale for the voice", seen.Locale)
	}
	// Ten seconds of reference is the design point; a much shorter line would
	// teach the model too little and a longer one slows every turn.
	if secs := float64(len(seen.Samples)) / float64(seen.SampleRate); secs < 5 || secs > 30 {
		t.Errorf("bootstrap produced %.1fs of audio", secs)
	}
	if names := config.Audio8RegisteredVoiceNames(modelsDir); strings.Join(names, ",") != "english" {
		t.Errorf("store holds %v, want [english]", names)
	}
}

func TestVoicesRegisterRefusesAnExistingNameWithoutOverwrite(t *testing.T) {
	modelsDir := newAudio8TestConfig(t)
	installTestAudio8Export(t, modelsDir)
	seen := fakeRegistration(t)

	if _, err := runVoicesSub(t, "register", "mine", "--from-kokoro", "af_heart"); err != nil {
		t.Fatalf("first register: %v", err)
	}
	if seen.Overwrite {
		t.Error("--overwrite defaulted to true")
	}
	if _, err := runVoicesSub(t, "register", "mine", "--from-kokoro", "af_heart", "--overwrite"); err != nil {
		t.Fatalf("register --overwrite: %v", err)
	}
	if !seen.Overwrite {
		t.Error("--overwrite did not reach the registration")
	}
}

func TestVoicesRegisteredListsProvenance(t *testing.T) {
	modelsDir := newAudio8TestConfig(t)
	installTestAudio8Export(t, modelsDir)
	fakeRegistration(t)

	out, err := runVoicesSub(t, "registered", "--json")
	if err != nil {
		t.Fatalf("registered: %v", err)
	}
	var empty struct {
		Voices []audio8VoiceJSON `json:"voices"`
	}
	if err := json.Unmarshal([]byte(out), &empty); err != nil {
		t.Fatalf("parse: %v (%s)", err, out)
	}
	if len(empty.Voices) != 0 {
		t.Fatalf("an empty store lists %+v", empty.Voices)
	}

	if _, err := runVoicesSub(t, "register", "english", "--from-kokoro", "af_heart"); err != nil {
		t.Fatal(err)
	}
	out, err = runVoicesSub(t, "registered", "--json")
	if err != nil {
		t.Fatalf("registered: %v", err)
	}
	var listed struct {
		Voices []audio8VoiceJSON `json:"voices"`
	}
	if err := json.Unmarshal([]byte(out), &listed); err != nil {
		t.Fatalf("parse: %v (%s)", err, out)
	}
	if len(listed.Voices) != 1 || listed.Voices[0].Provenance != tts.Audio8VoiceSynthetic {
		t.Fatalf("registered = %+v, want one synthetic voice", listed.Voices)
	}
	if listed.Voices[0].Path != config.Audio8VoiceDir(modelsDir, "english") {
		t.Errorf("path = %q, want the store directory", listed.Voices[0].Path)
	}
}

func TestVoicesRemove(t *testing.T) {
	modelsDir := newAudio8TestConfig(t)
	installTestAudio8Export(t, modelsDir)
	fakeRegistration(t)
	if _, err := runVoicesSub(t, "register", "english", "--from-kokoro", "af_heart"); err != nil {
		t.Fatal(err)
	}

	if _, err := runVoicesSub(t, "remove", "nobody"); err == nil {
		t.Error("remove of an unknown voice error = nil")
	}
	if _, err := runVoicesSub(t, "remove", config.Audio8PackagedVoice); err == nil {
		t.Error("remove of the packaged reference error = nil; it is part of the export")
	}
	out, err := runVoicesSub(t, "remove", "english", "--json")
	if err != nil {
		t.Fatalf("remove: %v", err)
	}
	var row audio8VoiceJSON
	if err := json.Unmarshal([]byte(out), &row); err != nil {
		t.Fatalf("parse: %v (%s)", err, out)
	}
	if !row.Removed || row.Name != "english" {
		t.Errorf("json = %+v, want english removed", row)
	}
	if names := config.Audio8RegisteredVoiceNames(modelsDir); len(names) != 0 {
		t.Errorf("after removal the store holds %v", names)
	}
}

// A registered voice must reach every catalog surface without loading a model,
// and the packaged reference must stay first.
func TestRegisteredVoicesReachTheStaticCatalog(t *testing.T) {
	modelsDir := newAudio8TestConfig(t)
	installTestAudio8Export(t, modelsDir)
	fakeRegistration(t)
	if _, err := runVoicesSub(t, "register", "english", "--from-kokoro", "af_heart"); err != nil {
		t.Fatal(err)
	}

	names, err := ttsVoiceCatalog(config.Audio8ProviderName)
	if err != nil {
		t.Fatalf("ttsVoiceCatalog: %v", err)
	}
	if strings.Join(names, ",") != config.Audio8PackagedVoice+",english" {
		t.Fatalf("catalog = %v, want the packaged reference then english", names)
	}
	// And persona validation, which is what refused an audio8 voice before.
	if err := validateVoiceForProvider(config.Audio8ProviderName, "english"); err != nil {
		t.Errorf("validateVoiceForProvider(audio8, english) = %v, want nil", err)
	}
	if err := validateVoiceForProvider(config.Audio8ProviderName, "nobody"); err == nil {
		t.Error("validateVoiceForProvider(audio8, nobody) = nil, want a rejection")
	}
}

func TestActiveVoiceForAudio8(t *testing.T) {
	tests := []struct {
		name string
		cfg  config.Config
		want string
	}{
		{"empty selects the packaged reference", config.Config{TTSProvider: "audio8"}, config.Audio8PackagedVoice},
		{"a registered voice", config.Config{TTSProvider: "audio8", Audio8Voice: "english"}, "english"},
		{
			"a stale kokoro voice is not audio8's",
			config.Config{TTSProvider: "audio8", TTSVoice: "af_heart"},
			config.Audio8PackagedVoice,
		},
		{"kokoro is unaffected", config.Config{TTSProvider: "kokoro", TTSVoice: "af_heart"}, "af_heart"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := activeVoiceFor(&tt.cfg); got != tt.want {
				t.Errorf("activeVoiceFor() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestVoicesRegisterCanonicalizesTheKokoroVoice is the provenance-truth check:
// Kokoro looks its voice up by exact map key with a silent af_heart fallback,
// so "AF_SKY" used to speak as af_heart while the store recorded AF_SKY.
func TestVoicesRegisterCanonicalizesTheKokoroVoice(t *testing.T) {
	modelsDir := newAudio8TestConfig(t)
	installTestAudio8Export(t, modelsDir)
	seen := fakeRegistration(t)
	synthesized = ""

	if _, err := runVoicesSub(t, "register", "shouty", "--from-kokoro", "AF_SKY"); err != nil {
		t.Fatalf("register: %v", err)
	}
	if synthesized != "af_sky" {
		t.Errorf("Kokoro was asked for %q, want the canonical af_sky (an unresolvable name speaks as af_heart)", synthesized)
	}
	if seen.Provenance.BootstrapVoice != "af_sky" {
		t.Errorf("provenance records %q, want the voice that actually synthesized (af_sky)", seen.Provenance.BootstrapVoice)
	}
	if seen.Provenance.BootstrapVoice != synthesized {
		t.Errorf("provenance %q disagrees with the voice that spoke %q", seen.Provenance.BootstrapVoice, synthesized)
	}
	// VoiceInfo keys its language table on the lowercase first byte, so an
	// uppercased name would have landed here with no locale at all.
	if seen.Locale != "en-US" {
		t.Errorf("locale = %q, want en-US from the canonical name", seen.Locale)
	}
}

func TestCanonicalKokoroVoice(t *testing.T) {
	tests := []struct {
		name   string
		in     string
		want   string
		wantOK bool
	}{
		{name: "empty", in: ""},
		{name: "unknown", in: "af_nonesuch"},
		{name: "whitespace only", in: "   "},
		{name: "exact", in: "af_heart", want: "af_heart", wantOK: true},
		{name: "uppercase", in: "AF_SKY", want: "af_sky", wantOK: true},
		{name: "mixed case", in: "Bm_George", want: "bm_george", wantOK: true},
		{name: "padded", in: "  af_bella  ", want: "af_bella", wantOK: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := canonicalKokoroVoice(tt.in)
			if ok != tt.wantOK || got != tt.want {
				t.Errorf("canonicalKokoroVoice(%q) = %q, %v; want %q, %v", tt.in, got, ok, tt.want, tt.wantOK)
			}
		})
	}
}

// The size gate has to run before the decode and before ~400 MB of encoder
// weights, so an oversized file is refused without either being paid for.
func TestVoicesRegisterRefusesAnOversizedReferenceBeforeReadingIt(t *testing.T) {
	modelsDir := newAudio8TestConfig(t)
	installTestAudio8Export(t, modelsDir)
	fakeRegistration(t)

	read := false
	voiceRegister.readWAV = func(string) ([]float32, int, error) {
		read = true
		return make([]float32, 44100), 44100, nil
	}
	voiceRegister.stat = func(string) (os.FileInfo, error) {
		return fakeFileInfo{size: 52428801}, nil // one byte over the manifest cap
	}
	_, err := runVoicesSub(t, "register", "big", "--wav", "big.wav", "--text", "hello there", "--consent")
	if err == nil || !strings.Contains(err.Error(), "over the export's") {
		t.Fatalf("register error = %v, want the size refusal", err)
	}
	if read {
		t.Error("the WAV was decoded before the size gate refused it")
	}

	voiceRegister.stat = func(string) (os.FileInfo, error) {
		return fakeFileInfo{size: 52428800, mode: fs.ModeDir}, nil
	}
	_, err = runVoicesSub(t, "register", "dir", "--wav", "somedir", "--text", "hello there", "--consent")
	if err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("register error = %v, want a regular-file refusal", err)
	}
}
