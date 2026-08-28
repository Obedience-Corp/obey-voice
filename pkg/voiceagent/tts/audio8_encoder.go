package tts

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/onnxrt"
)

// audio8RegistrationSubdir holds the codec encoder and its manifest inside the
// export. The vendor's own runtime expects them at the export root; the
// published snapshot ships them one level down.
const audio8RegistrationSubdir = "registration"

const audio8RegistrationManifestFile = "registration_manifest.json"

// Reference-audio duration bounds, mirroring the vendor's registration path
// (Audio8-AI/Audio8_TTS @ c856e38, arktts_runtime/registration.py). They are
// not arbitrary: the reference frames are prepended to every prompt, so a long
// reference pays for itself on the prefix prime and again on every turn's
// attention, and eventually collides with the export's 2048-token max_seq_len.
const (
	audio8MinReferenceSeconds = 0.5
	audio8MaxReferenceSeconds = 30.0
)

// audio8RegistrationManifest is registration_manifest.json.
type audio8RegistrationManifest struct {
	ModelFingerprint string `json:"model_fingerprint"`
	SampleRate       int    `json:"sample_rate"`
	NumCodebooks     int    `json:"num_codebooks"`
	Encoder          string `json:"encoder"`
	MaxAudioBytes    int64  `json:"max_audio_bytes"`
}

func audio8RegistrationDir(modelDir string) string {
	return filepath.Join(modelDir, audio8RegistrationSubdir)
}

// loadAudio8RegistrationManifest reads and range-checks the registration
// manifest. It does not compare fingerprints: that needs the runtime manifest
// too, and belongs to the caller that has both.
func loadAudio8RegistrationManifest(modelDir string) (*audio8RegistrationManifest, error) {
	path := filepath.Join(audio8RegistrationDir(modelDir), audio8RegistrationManifestFile)
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("audio8: read %s: %w", audio8RegistrationManifestFile, err)
	}
	var m audio8RegistrationManifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("audio8: parse %s: %w", audio8RegistrationManifestFile, err)
	}
	if m.NumCodebooks <= 0 || m.SampleRate <= 0 || m.Encoder == "" {
		return nil, fmt.Errorf("audio8: %s is missing encoder, sample_rate or num_codebooks", audio8RegistrationManifestFile)
	}
	return &m, nil
}

// Audio8Encoder turns reference audio into the codec frames a voice is made of.
//
// It is a separate session from the provider's three graphs and is opened only
// when a voice is being registered: the encoder is another 400 MB of weights,
// and speaking never needs it.
type Audio8Encoder struct {
	env  *onnxrt.Env
	sess *onnxrt.Session
	man  *audio8RegistrationManifest
	hop  int // codec frame size, from the runtime manifest
}

// NewAudio8Encoder opens the codec encoder in modelDir's registration package,
// refusing an encoder whose fingerprint does not match the runtime manifest
// beside it. Codes registered against a different model revision would decode
// as noise, so the mismatch is refused here rather than discovered at synthesis
// time.
func NewAudio8Encoder(modelDir string) (*Audio8Encoder, error) {
	if modelDir == "" {
		return nil, fmt.Errorf("audio8: model dir is required")
	}
	runtime, err := loadAudio8Manifest(modelDir)
	if err != nil {
		return nil, err
	}
	man, err := loadAudio8RegistrationManifest(modelDir)
	if err != nil {
		return nil, err
	}
	if err := checkAudio8Fingerprint(runtime.ModelFingerprint, man.ModelFingerprint, "registration package"); err != nil {
		return nil, err
	}
	if man.NumCodebooks != runtime.NumCodebooks {
		return nil, fmt.Errorf("audio8: registration manifest declares %d codebooks, the runtime manifest %d",
			man.NumCodebooks, runtime.NumCodebooks)
	}
	if man.SampleRate != runtime.SampleRate {
		return nil, fmt.Errorf("audio8: registration manifest declares %d Hz, the runtime manifest %d Hz",
			man.SampleRate, runtime.SampleRate)
	}

	env, err := onnxrt.NewEnv()
	if err != nil {
		return nil, err
	}
	// The encoder is one large graph over the whole clip, like the decoder, so
	// it takes the codec thread count rather than the AR one.
	sess, err := onnxrt.NewSession(env, filepath.Join(audio8RegistrationDir(modelDir), man.Encoder), 10, 5, []string{"codes"})
	if err != nil {
		env.Close()
		return nil, err
	}
	hop := runtime.CodecFrameSize
	if hop <= 0 {
		hop = runtime.CodecHopLength
	}
	return &Audio8Encoder{env: env, sess: sess, man: man, hop: hop}, nil
}

// Close releases the session and environment.
func (e *Audio8Encoder) Close() {
	if e == nil {
		return
	}
	if e.sess != nil {
		e.sess.Close()
		e.sess = nil
	}
	if e.env != nil {
		e.env.Close()
		e.env = nil
	}
}

// SampleRate is the rate the encoder expects; Encode resamples to it.
func (e *Audio8Encoder) SampleRate() int { return e.man.SampleRate }

// Fingerprint is the model revision codes from this encoder belong to.
func (e *Audio8Encoder) Fingerprint() string { return e.man.ModelFingerprint }

// Encode converts mono reference audio into [numCodebooks][frames] codes.
// samples may be at any rate; they are resampled to the encoder's.
func (e *Audio8Encoder) Encode(ctx context.Context, samples []float32, rate int) ([][]int64, error) {
	if e == nil || e.sess == nil {
		return nil, fmt.Errorf("audio8: encoder is closed")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if rate <= 0 {
		return nil, fmt.Errorf("audio8: reference audio sample rate %d is not valid", rate)
	}
	if len(samples) == 0 {
		return nil, fmt.Errorf("audio8: reference audio is empty")
	}
	seconds := float64(len(samples)) / float64(rate)
	if seconds < audio8MinReferenceSeconds || seconds > audio8MaxReferenceSeconds {
		return nil, fmt.Errorf("audio8: reference audio is %.2fs, want between %.1fs and %.0fs",
			seconds, audio8MinReferenceSeconds, audio8MaxReferenceSeconds)
	}
	for i, v := range samples {
		if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) {
			return nil, fmt.Errorf("audio8: reference audio sample %d is not finite", i)
		}
	}

	pcm, err := resampleBandlimited(samples, rate, e.man.SampleRate)
	if err != nil {
		return nil, fmt.Errorf("audio8: %w", err)
	}
	// max_audio_bytes bounds the *source file* - that is what the vendor's own
	// registration applies it to - so the CLI checks it on a stat before
	// anything is read (see checkReferenceFileSize). This is the backstop for
	// callers that supply samples directly: the tensor is fed as float16, so
	// two bytes a sample, not four. The 30 s duration cap above already keeps
	// this well under 50 MiB; it exists so a future cap change cannot quietly
	// hand the graph more than the manifest allows.
	if e.man.MaxAudioBytes > 0 && int64(len(pcm))*2 > e.man.MaxAudioBytes {
		return nil, fmt.Errorf("audio8: reference audio is %d encoder bytes at %d Hz, over the manifest's %d byte limit",
			len(pcm)*2, e.man.SampleRate, e.man.MaxAudioBytes)
	}
	// The codec consumes whole hops; the vendor's registration path pads the
	// tail the same way rather than dropping the last partial frame.
	if e.hop > 0 {
		if rem := len(pcm) % e.hop; rem != 0 {
			pcm = append(pcm, make([]float32, e.hop-rem)...)
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	out, err := e.sess.Run(map[string]*onnxrt.Tensor{
		"audio": onnxrt.F16Tensor(pcm, 1, 1, int64(len(pcm))),
	})
	if err != nil {
		return nil, fmt.Errorf("audio8: codec encode: %w", err)
	}
	return audio8CodesFromOutput(out[0], e.man.NumCodebooks)
}

// audio8CodesFromOutput reshapes an encoder result of [1, codebooks, frames]
// (or [codebooks, frames]) into rows.
func audio8CodesFromOutput(out onnxrt.Output, wantCodebooks int) ([][]int64, error) {
	if out.I64 == nil {
		return nil, fmt.Errorf("audio8: encoder returned %d float values, want int64 codes", len(out.Data))
	}
	shape := out.Shape
	for len(shape) > 2 && shape[0] == 1 {
		shape = shape[1:]
	}
	if len(shape) != 2 {
		return nil, fmt.Errorf("audio8: encoder returned codes of shape %v, want [%d, frames]", out.Shape, wantCodebooks)
	}
	rows, frames := int(shape[0]), int(shape[1])
	if rows != wantCodebooks || frames <= 0 {
		return nil, fmt.Errorf("audio8: encoder returned codes of shape %v, want [%d, frames]", out.Shape, wantCodebooks)
	}
	if len(out.I64) < rows*frames {
		return nil, fmt.Errorf("audio8: encoder returned %d codes for shape %v", len(out.I64), out.Shape)
	}
	codes := make([][]int64, rows)
	for r := range codes {
		codes[r] = make([]int64, frames)
		copy(codes[r], out.I64[r*frames:(r+1)*frames])
	}
	return codes, nil
}

// checkAudio8Fingerprint refuses codes from a different model revision. what
// names the thing being checked so the message says which half is stale.
func checkAudio8Fingerprint(runtime, other, what string) error {
	if runtime == "" || other == "" {
		return fmt.Errorf("audio8: %s has no model_fingerprint; refusing to assume it matches the installed export", what)
	}
	if runtime != other {
		return fmt.Errorf("audio8: %s belongs to model %q but the installed export is %q", what, other, runtime)
	}
	return nil
}

// Audio8MaxReferenceBytes is the largest reference file the export's
// registration package accepts, from the manifest's max_audio_bytes. Zero means
// the manifest declares no limit.
//
// It reads one small JSON file and opens no graph, so a caller can reject an
// oversized file before paying for the decode or the encoder weights.
func Audio8MaxReferenceBytes(modelDir string) (int64, error) {
	man, err := loadAudio8RegistrationManifest(modelDir)
	if err != nil {
		return 0, err
	}
	return man.MaxAudioBytes, nil
}
