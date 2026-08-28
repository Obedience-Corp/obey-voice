package tts

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

// Audio8RegisterRequest describes one voice registration.
//
// Samples are the reference audio at SampleRate, already mono - the encoder
// resamples but does not downmix, because a caller that read a stereo file
// knows how it wants the channels folded and the WAV reader here already
// averages them.
type Audio8RegisterRequest struct {
	// ModelDir is the Audio8 export; ModelsDir is its parent, under which the
	// voice store lives. They are separate because the export is routinely a
	// symlink into a read-only HuggingFace cache.
	ModelDir  string
	ModelsDir string

	Name       string
	Transcript string
	Locale     string

	Samples    []float32
	SampleRate int

	Provenance Audio8VoiceProvenance
	Overwrite  bool
}

// RegisterAudio8Voice encodes reference audio and stores it as a named voice.
//
// Nothing here is specific to a person: the caller supplies the audio, and the
// only reference this repository ships is the one packaged inside the export.
// That is deliberate - see docs/audio8-voices.md.
func RegisterAudio8Voice(ctx context.Context, req Audio8RegisterRequest) (Audio8VoiceMeta, error) {
	name, err := config.CanonicalAudio8VoiceName(req.Name)
	if err != nil {
		return Audio8VoiceMeta{}, err
	}
	req.Name = name
	// SaveAudio8Voice collapses it; the check here is so a caller learns before
	// the encoder pages in 400 MB of weights.
	transcript := req.Transcript
	if strings.TrimSpace(transcript) == "" {
		return Audio8VoiceMeta{}, fmt.Errorf("audio8: voice %q needs the transcript of its reference audio", req.Name)
	}
	if strings.TrimSpace(req.ModelsDir) == "" {
		return Audio8VoiceMeta{}, fmt.Errorf("audio8: models dir is required")
	}
	if !req.Overwrite {
		if _, err := os.Stat(filepath.Join(config.Audio8VoiceDir(req.ModelsDir, req.Name), config.Audio8VoiceMetaFile)); err == nil {
			return Audio8VoiceMeta{}, fmt.Errorf("audio8: voice %q already exists; pass --overwrite to replace it", req.Name)
		}
	}

	encoder, err := NewAudio8Encoder(req.ModelDir)
	if err != nil {
		return Audio8VoiceMeta{}, err
	}
	defer encoder.Close()

	codes, err := encoder.Encode(ctx, req.Samples, req.SampleRate)
	if err != nil {
		return Audio8VoiceMeta{}, err
	}

	provenance := req.Provenance
	if provenance.Kind == "" {
		provenance.Kind = Audio8VoiceRecorded
	}
	if provenance.SourceSampleRate == 0 {
		provenance.SourceSampleRate = req.SampleRate
	}
	if provenance.SourceSeconds == 0 && req.SampleRate > 0 {
		provenance.SourceSeconds = float64(len(req.Samples)) / float64(req.SampleRate)
	}
	if provenance.Kind == Audio8VoiceSynthetic {
		// There is no one to have consented: saying otherwise in the record
		// would be the one lie this file exists to prevent.
		provenance.Consent = false
	}

	meta := Audio8VoiceMeta{
		Name:             req.Name,
		Transcript:       transcript,
		Locale:           strings.TrimSpace(req.Locale),
		ModelFingerprint: encoder.Fingerprint(),
		SampleRate:       encoder.SampleRate(),
		Provenance:       provenance,
	}
	if err := SaveAudio8Voice(req.ModelsDir, meta, codes); err != nil {
		return Audio8VoiceMeta{}, err
	}
	// Read back rather than returning the in-memory value: the caller prints
	// what is on disk, including the fields SaveAudio8Voice filled in.
	return LoadAudio8VoiceMeta(req.ModelsDir, req.Name)
}

// Audio8RegistrationInstalled reports whether modelDir carries a usable
// registration package. It reads two small JSON files and opens no graph, so
// doctor and the capability row can call it freely.
func Audio8RegistrationInstalled(modelDir string) bool {
	runtime, err := loadAudio8Manifest(modelDir)
	if err != nil {
		return false
	}
	man, err := loadAudio8RegistrationManifest(modelDir)
	if err != nil {
		return false
	}
	if checkAudio8Fingerprint(runtime.ModelFingerprint, man.ModelFingerprint, "registration package") != nil {
		return false
	}
	_, err = os.Stat(filepath.Join(audio8RegistrationDir(modelDir), man.Encoder))
	return err == nil
}

// Audio8SourceDigest is the SHA-256 of a reference file, recorded in a voice's
// provenance so the same recording is recognisable across registrations.
func Audio8SourceDigest(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	sum := sha256.New()
	if _, err := io.Copy(sum, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(sum.Sum(nil)), nil
}
