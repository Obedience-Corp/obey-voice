//go:build !integration

package cmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/tts"
)

// audio8BootstrapText is the default line the Kokoro bootstrap speaks.
//
// It is about ten seconds of neutral English chosen for phonetic coverage
// rather than meaning: every English vowel, both dental fricatives, the
// affricates, and the liquids and nasals in several positions. The reference
// only teaches the model a timbre, so a line that never voices a sound leaves
// that sound to be invented.
const audio8BootstrapText = "The quick brown fox jumps over the lazy dog while the children watch " +
	"through thin glass. Judge my vow, she thought, and measure the yellow " +
	"light before it fades."

// voiceRegisterDeps are the heavy parts of registration: encoding needs the
// export's codec encoder, and the bootstrap needs a whole Kokoro model. Tests
// swap them so the flag contract, the consent gate and the store layout are
// exercised without either model on disk.
type voiceRegisterDeps struct {
	register   func(context.Context, tts.Audio8RegisterRequest) (tts.Audio8VoiceMeta, error)
	readWAV    func(string) ([]float32, int, error)
	digest     func(string) (string, error)
	stat       func(string) (os.FileInfo, error)
	synthesize func(context.Context, *config.Config, string, string) ([]float32, int, error)
}

var voiceRegister = voiceRegisterDeps{
	register:   tts.RegisterAudio8Voice,
	readWAV:    audio.ReadWAVFloat32,
	digest:     tts.Audio8SourceDigest,
	stat:       os.Stat,
	synthesize: synthesizeBootstrapAudio,
}

// audio8VoiceJSON is the body of the register/registered/remove verbs.
type audio8VoiceJSON struct {
	Name        string  `json:"name"`
	Transcript  string  `json:"transcript"`
	Locale      string  `json:"locale,omitempty"`
	Provenance  string  `json:"provenance"`
	Source      string  `json:"source,omitempty"`
	Consent     bool    `json:"consent"`
	Frames      int     `json:"frames"`
	Seconds     float64 `json:"seconds"`
	Fingerprint string  `json:"model_fingerprint"`
	CreatedAt   string  `json:"created_at"`
	Path        string  `json:"path"`
	Removed     bool    `json:"removed,omitempty"`
	// RemovedPaths names every directory a removal deleted. It is more than
	// Path whenever a legacy, non-canonical spelling of the same voice was on
	// disk beside it - the only way a caller can see that one existed.
	RemovedPaths []string `json:"removed_paths,omitempty"`
}

func newVoicesRegisterCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "register NAME",
		Short: "Register a reference voice for the audio8 provider",
		Long: `Encode a reference recording into a named audio8 voice.

A voice is a short piece of audio plus its exact transcript. Obey Voice ships
no reference recording of any person: the audio is yours to supply, and by
passing --consent you state that you have the right to use that voice. Register
someone else's voice only with their permission.

--from-kokoro needs no recording at all: Kokoro speaks the reference line here,
on this computer, and the result is registered as a synthetic voice. Nobody's
voice is cloned, so --consent does not apply.

Reference audio must be between 0.5 and 30 seconds. Around ten seconds is the
useful range: the reference is prepended to every prompt, so a longer one makes
both the one-time prime and every turn afterwards slower.

Examples:
  obey-voice voices register mine --wav ref.wav --text "exactly what is said" --consent
  obey-voice voices register english --from-kokoro af_heart`,
		Args:          cobra.ExactArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE:          runVoicesRegister,
	}
	cmd.Flags().String("wav", "", "Reference recording (16-bit PCM WAV; any rate, mono or stereo)")
	cmd.Flags().String("text", "", "Exact transcript of the reference audio (required with --wav)")
	cmd.Flags().String("from-kokoro", "", "Synthesize the reference with this Kokoro voice instead of supplying a recording")
	cmd.Flags().String("locale", "", "Locale of the reference audio, e.g. en-US (used to filter `voices --locale`)")
	cmd.Flags().Bool("consent", false, "Affirm you have permission to use the voice in --wav")
	cmd.Flags().Bool("overwrite", false, "Replace an existing voice of the same name")
	cmd.Flags().Bool("json", false, "Emit JSON")
	return cmd
}

func runVoicesRegister(cmd *cobra.Command, args []string) error {
	cmd.SilenceUsage = true
	asJSON, _ := cmd.Flags().GetBool("json")
	meta, cfg, err := registerAudio8Voice(cmd, args[0])
	if err != nil {
		if asJSON {
			return emitJSONError(cmd, err)
		}
		return err
	}
	row := audio8VoiceRow(cfg, meta)
	if asJSON {
		return encodeJSON(cmd, row)
	}
	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "\n  %s\n", titleStyle.Render("Registered: "+meta.Name))
	printAudio8VoiceRow(cmd, row)
	fmt.Fprintf(out, "  %s\n\n", dimStyle.Render(
		"speak with it: obey-voice config set audio8_voice "+meta.Name))
	return nil
}

func registerAudio8Voice(cmd *cobra.Command, name string) (tts.Audio8VoiceMeta, *config.Config, error) {
	cfg, err := config.Load()
	if err != nil {
		return tts.Audio8VoiceMeta{}, nil, err
	}
	name, err = config.CanonicalAudio8VoiceName(name)
	if err != nil {
		return tts.Audio8VoiceMeta{}, nil, codedError(codeInvalidVoice, "%v", err)
	}

	wav, _ := cmd.Flags().GetString("wav")
	bootstrap, _ := cmd.Flags().GetString("from-kokoro")
	wav, bootstrap = strings.TrimSpace(wav), strings.TrimSpace(bootstrap)
	text, _ := cmd.Flags().GetString("text")
	locale, _ := cmd.Flags().GetString("locale")
	consent, _ := cmd.Flags().GetBool("consent")
	overwrite, _ := cmd.Flags().GetBool("overwrite")

	switch {
	case wav == "" && bootstrap == "":
		return tts.Audio8VoiceMeta{}, nil, usageError("give the reference audio: --wav FILE with --text, or --from-kokoro VOICE")
	case wav != "" && bootstrap != "":
		return tts.Audio8VoiceMeta{}, nil, usageError("--wav and --from-kokoro are alternatives; pass one")
	}

	modelDir := filepath.Join(config.ModelsDirFrom(cfg), config.Audio8Subdir)
	if !config.Audio8ExportInstalled(modelDir) {
		return tts.Audio8VoiceMeta{}, nil, codedError(codeAssetsMissing,
			"no Audio8 export at %s (obey-voice does not download it; snapshot %s @ %s)",
			modelDir, config.Audio8SnapshotRepo, config.Audio8SnapshotRev)
	}

	req := tts.Audio8RegisterRequest{
		ModelDir:  modelDir,
		ModelsDir: config.ModelsDirFrom(cfg),
		Name:      name,
		Locale:    strings.TrimSpace(locale),
		Overwrite: overwrite,
	}

	if bootstrap != "" {
		if consent {
			// The help says consent does not apply here; accepting and dropping
			// it would leave the user believing they recorded something.
			return tts.Audio8VoiceMeta{}, nil, usageError(
				"--consent does not apply to --from-kokoro: the reference is synthesized here, so there is nobody to have given it")
		}
		// Canonicalize before anything records the name. Kokoro resolves its
		// voice by exact map key and silently falls back to af_heart, so an
		// almost-right name would speak in one voice and be recorded as another.
		canonical, ok := canonicalKokoroVoice(bootstrap)
		if !ok {
			return tts.Audio8VoiceMeta{}, nil, codedError(codeInvalidVoice,
				"--from-kokoro %q is not a Kokoro voice (try `obey-voice voices --provider kokoro`)", bootstrap)
		}
		if strings.TrimSpace(text) == "" {
			text = audio8BootstrapText
		}
		samples, rate, err := voiceRegister.synthesize(cmd.Context(), cfg, canonical, text)
		if err != nil {
			return tts.Audio8VoiceMeta{}, nil, err
		}
		req.Samples, req.SampleRate, req.Transcript = samples, rate, text
		if req.Locale == "" {
			// Kokoro's catalog is locale-tagged, so the synthesized reference
			// inherits a locale we actually know rather than guessing one.
			req.Locale = tts.VoiceInfo(canonical).Locale
		}
		req.Provenance = tts.Audio8VoiceProvenance{
			Kind:              tts.Audio8VoiceSynthetic,
			BootstrapProvider: "kokoro",
			BootstrapVoice:    canonical,
		}
		meta, err := voiceRegister.register(cmd.Context(), req)
		return meta, cfg, err
	}

	if strings.TrimSpace(text) == "" {
		return tts.Audio8VoiceMeta{}, nil, usageError("--text is required with --wav: the model needs the exact transcript of the reference audio")
	}
	if !consent {
		// The one thing this command must not do is make cloning a stranger's
		// voice frictionless. The flag is the record that someone said yes.
		return tts.Audio8VoiceMeta{}, nil, codedError(codeConfirmRequired,
			"pass --consent to confirm you have permission to use the voice in %s", wav)
	}
	if err := checkReferenceFileSize(modelDir, wav); err != nil {
		return tts.Audio8VoiceMeta{}, nil, err
	}
	samples, rate, err := voiceRegister.readWAV(wav)
	if err != nil {
		return tts.Audio8VoiceMeta{}, nil, fmt.Errorf("reading %s: %w", wav, err)
	}
	digest, err := voiceRegister.digest(wav)
	if err != nil {
		return tts.Audio8VoiceMeta{}, nil, fmt.Errorf("hashing %s: %w", wav, err)
	}
	req.Samples, req.SampleRate, req.Transcript = samples, rate, text
	req.Provenance = tts.Audio8VoiceProvenance{
		Kind:         tts.Audio8VoiceRecorded,
		SourceFile:   filepath.Base(wav),
		SourceSHA256: digest,
		Consent:      true,
	}
	meta, err := voiceRegister.register(cmd.Context(), req)
	return meta, cfg, err
}

// checkReferenceFileSize refuses an oversized reference before anything reads
// it. The manifest's max_audio_bytes bounds the source file - it is the limit
// the vendor's own registration applies to the uploaded bytes - so it is
// checked here, on a stat, rather than after a full decode, a resample and
// ~400 MB of encoder weights.
func checkReferenceFileSize(modelDir, path string) error {
	limit, err := tts.Audio8MaxReferenceBytes(modelDir)
	if err != nil {
		return err
	}
	info, err := voiceRegister.stat(path)
	if err != nil {
		return fmt.Errorf("reading %s: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", path)
	}
	if limit > 0 && info.Size() > limit {
		return codedError(codeInvalidInput,
			"%s is %d bytes, over the export's %d byte reference limit", path, info.Size(), limit)
	}
	return nil
}

func newVoicesRegisteredCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "registered",
		Short: "List registered audio8 voices with their provenance",
		Long: `Show every voice registered for the audio8 provider.

Unlike ` + "`voices`" + `, which lists what can be spoken, this reports where each
voice came from: a recording someone consented to, or audio this computer
synthesized.`,
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE:          runVoicesRegistered,
	}
	cmd.Flags().Bool("json", false, "Emit JSON")
	return cmd
}

func runVoicesRegistered(cmd *cobra.Command, _ []string) error {
	cmd.SilenceUsage = true
	asJSON, _ := cmd.Flags().GetBool("json")
	cfg, err := config.Load()
	if err != nil {
		if asJSON {
			return emitJSONError(cmd, err)
		}
		return err
	}
	metas, err := tts.ListAudio8Voices(config.ModelsDirFrom(cfg))
	if err != nil {
		if asJSON {
			return emitJSONError(cmd, err)
		}
		return err
	}
	rows := make([]audio8VoiceJSON, 0, len(metas))
	for _, meta := range metas {
		rows = append(rows, audio8VoiceRow(cfg, meta))
	}
	if asJSON {
		return encodeJSON(cmd, map[string]any{"voices": rows})
	}
	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "\n  %s %s\n\n", titleStyle.Render("Registered audio8 voices:"),
		sectionStyle.Render(config.Audio8VoicesDirFrom(cfg)))
	if len(rows) == 0 {
		fmt.Fprintf(out, "  %s\n\n", dimStyle.Render(
			"None. The packaged reference is the only voice; register one with `obey-voice voices register`."))
		return nil
	}
	for _, row := range rows {
		fmt.Fprintf(out, "  %s %s\n", keyStyle.Render(fmt.Sprintf("%-16s", row.Name)),
			dimStyle.Render(fmt.Sprintf("%s · %.1fs · %s", row.Provenance, row.Seconds, row.CreatedAt)))
	}
	fmt.Fprintf(out, "\n  %s\n\n", dimStyle.Render(fmt.Sprintf("%d registered.", len(rows))))
	return nil
}

func newVoicesRemoveCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "remove NAME",
		Short: "Delete a registered audio8 voice",
		Long: `Delete one registered voice by name.

Only registered voices can be removed. The voice packaged with the export is
part of the model, not the store, and is never touched.`,
		Args:          cobra.ExactArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE:          runVoicesRemove,
	}
	cmd.Flags().Bool("json", false, "Emit JSON")
	return cmd
}

func runVoicesRemove(cmd *cobra.Command, args []string) error {
	cmd.SilenceUsage = true
	asJSON, _ := cmd.Flags().GetBool("json")
	fail := func(err error) error {
		if asJSON {
			return emitJSONError(cmd, err)
		}
		return err
	}
	cfg, err := config.Load()
	if err != nil {
		return fail(err)
	}
	name, err := config.CanonicalAudio8VoiceName(args[0])
	if err != nil {
		return fail(codedError(codeInvalidVoice, "%v", err))
	}
	modelsDir := config.ModelsDirFrom(cfg)
	// Read the metadata before the removal, but do not require it: a directory
	// registered before names were canonicalized ("  bob  ") cannot be loaded,
	// because the store builds its path from the canonical name. It is still on
	// disk holding that name, so removal has to reach it or nothing will.
	meta, metaErr := tts.LoadAudio8VoiceMeta(modelsDir, name)
	removed, err := tts.RemoveAudio8Voice(modelsDir, name)
	if err != nil {
		if metaErr != nil {
			return fail(codedError(codeNotFound, "%v", metaErr))
		}
		return fail(err)
	}
	if metaErr != nil {
		meta = tts.Audio8VoiceMeta{Name: name}
	}
	row := audio8VoiceRow(cfg, meta)
	row.Removed = true
	row.RemovedPaths = removed
	if asJSON {
		return encodeJSON(cmd, row)
	}
	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "\n  Removed %s\n", meta.Name)
	for _, path := range removed {
		if path != config.Audio8VoiceDir(modelsDir, name) {
			fmt.Fprintf(out, "  %s\n", dimStyle.Render(
				"also removed "+path+", registered under a spelling this store cannot resolve"))
		}
	}
	if strings.EqualFold(strings.TrimSpace(cfg.Audio8Voice), name) {
		fmt.Fprintf(out, "  %s\n", dimStyle.Render(
			"audio8_voice still names it; audio8 will refuse to speak until you change or unset that key"))
	}
	fmt.Fprintln(out)
	return nil
}

func audio8VoiceRow(cfg *config.Config, meta tts.Audio8VoiceMeta) audio8VoiceJSON {
	source := meta.Provenance.SourceFile
	if meta.Synthetic() {
		source = strings.TrimSpace(meta.Provenance.BootstrapProvider + " " + meta.Provenance.BootstrapVoice)
	}
	return audio8VoiceJSON{
		Name:        meta.Name,
		Transcript:  meta.Transcript,
		Locale:      meta.Locale,
		Provenance:  meta.Provenance.Kind,
		Source:      source,
		Consent:     meta.Provenance.Consent,
		Frames:      meta.Frames,
		Seconds:     meta.Seconds(),
		Fingerprint: meta.ModelFingerprint,
		CreatedAt:   meta.CreatedAt.UTC().Format("2006-01-02T15:04:05Z"),
		Path:        config.Audio8VoiceDir(config.ModelsDirFrom(cfg), meta.Name),
	}
}

func printAudio8VoiceRow(cmd *cobra.Command, row audio8VoiceJSON) {
	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "  provenance   %s", row.Provenance)
	if row.Source != "" {
		fmt.Fprintf(out, " (%s)", row.Source)
	}
	fmt.Fprintln(out)
	fmt.Fprintf(out, "  transcript   %s\n", row.Transcript)
	if row.Locale != "" {
		fmt.Fprintf(out, "  locale       %s\n", row.Locale)
	}
	fmt.Fprintf(out, "  reference    %d frames (%.1fs)\n", row.Frames, row.Seconds)
	fmt.Fprintf(out, "  model        %s\n", row.Fingerprint)
	fmt.Fprintf(out, "  path         %s\n", row.Path)
}
