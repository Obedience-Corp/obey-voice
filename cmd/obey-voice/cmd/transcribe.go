//go:build !integration

package cmd

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"os/exec"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/stt"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/transcription"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/transcription/protocol"
)

func init() {
	rootCmd.AddCommand(newTranscribeCmd(defaultTranscribeDeps()))
}

// transcribeSource is the file source the command drives: a frame source that
// also reports where it is and what it opened.
type transcribeSource interface {
	transcription.FrameSourceWithPosition
	Format() audio.WAVFormat
}

// transcribeDeps are the seams the command is built and tested through.
// Nothing here runs until RunE calls it, which is what lets the command tests
// cover every path with no model on disk.
type transcribeDeps struct {
	loadConfig     func() (*config.Config, error)
	manifest       func(cfg *config.Config) (config.AssetManifest, error)
	modelsDir      func(cfg *config.Config) string
	openSource     func(path string) (transcribeSource, error)
	newWordDecoder func(cfg *config.Config) (stt.SegmentDecoder, stt.DecoderInfo, func(), error)
	newDecoder     func(cfg *config.Config) (stt.SegmentDecoder, stt.DecoderInfo, func(), error)
	newDetector    func(cfg *config.Config) (transcription.SpanDetector, func(), error)
	run            func(ctx context.Context, req transcription.Request, deps transcription.Deps) (transcription.Summary, error)
	newContext     func() (context.Context, context.CancelFunc)
	diagnose       func(cfg *config.Config, modelsDir string) []config.Diagnostic
}

// fileVADSilence is how much silence closes an utterance in a file. It is
// shorter than the conversational vad_silence_duration: nothing is waiting for
// a turn to end here, and shorter silences give tighter caption boundaries.
const fileVADSilence = 600 * time.Millisecond

// fileVADMaxSpeech caps one utterance buffer. Continuous speech longer than
// this is split at the cap, not truncated.
const fileVADMaxSpeech = 20 * time.Second

func defaultTranscribeDeps() transcribeDeps {
	return transcribeDeps{
		loadConfig: config.Load,
		manifest: func(cfg *config.Config) (config.AssetManifest, error) {
			return config.ManifestFor(cfg, config.AssetRequest{NeedSTT: true, NeedVAD: true})
		},
		modelsDir: config.ModelsDirFrom,
		openSource: func(path string) (transcribeSource, error) {
			return audio.NewWAVFileSource(path, audio.ChunkSize)
		},
		newDecoder:     stt.NewSegmentDecoder,
		newWordDecoder: stt.NewWordDecoder,
		newDetector: func(cfg *config.Config) (transcription.SpanDetector, func(), error) {
			opts := audio.FileVADOptions{MinSilence: fileVADSilence, MaxSpeech: fileVADMaxSpeech}
			vad, err := audio.NewFileVAD(cfg, opts)
			if err != nil {
				return nil, nil, err
			}
			preRoll := time.Duration(cfg.VADPreRollMS) * time.Millisecond
			return transcription.NewFileSpanDetector(vad, opts, preRoll), vad.Delete, nil
		},
		run:        transcription.Run,
		newContext: signalContext,
		diagnose: func(cfg *config.Config, modelsDir string) []config.Diagnostic {
			return config.Diagnose(cfg, modelsDir, exec.LookPath)
		},
	}
}

type transcribeOptions struct {
	request string
	jsonl   bool
}

func newTranscribeCmd(deps transcribeDeps) *cobra.Command {
	var opts transcribeOptions
	var speechConfig string
	baseLoad := deps.loadConfig
	deps.loadConfig = func() (*config.Config, error) {
		if speechConfig != "" {
			return config.LoadSpeechFile(speechConfig)
		}
		return baseLoad()
	}
	cmd := &cobra.Command{
		Use: "transcribe",
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			if cmd.Name() == "setup" && speechConfig == "" {
				return fmt.Errorf("transcribe setup requires --speech-config")
			}
			if speechConfig != "" {
				return nil
			}
			if cmd.Root() == rootCmd {
				return rootCmd.PersistentPreRunE(cmd, args)
			}
			return nil
		},
		Short: "Transcribe a finished WAV as timed utterances or verbatim words",
		Long: `Transcribe a finished WAV and stream timed utterances as JSONL.

The job is described by a request file speaking obey-transcript/v1 (utterances)
or obey-transcript/v2 (verbatim word estimates with Parakeet), and the
answer is one JSON object per line on stdout: accepted, then a segment per
utterance, then exactly one of completed, failed or cancelled. Diagnostics go
to stderr. No microphone, agent turn, or download is involved.

This is not the Ask transcription surface: that one recognizes a live turn over
the serve WebSocket. See docs/transcribe-protocol.md.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runTranscribe(cmd, deps, opts)
		},
	}
	cmd.PersistentFlags().StringVar(&speechConfig, "speech-config", "", "Absolute path to isolated obey-voice.speech-config/v1 JSON; ignores global settings")
	f := cmd.Flags()
	f.StringVar(&opts.request, "request", "", "Absolute path to the obey-transcript/v1 or v2 request JSON")
	f.BoolVar(&opts.jsonl, "jsonl", false, "Stream negotiated transcript events as JSONL on stdout")
	cmd.AddCommand(newTranscribeCapabilitiesCmd(deps))
	cmd.AddCommand(newAudioJobCmd())
	cmd.AddCommand(&cobra.Command{Use: "setup", Short: "Install missing speech assets into the explicit speech configuration", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if speechConfig == "" {
				return fmt.Errorf("transcribe setup requires --speech-config")
			}
			cfg, err := deps.loadConfig()
			if err != nil {
				return err
			}
			ctx, cancel := deps.newContext()
			defer cancel()
			cmd.SetContext(ctx)
			return runModelsEnsureJSON(cmd, cfg, config.AssetRequest{NeedSTT: true, NeedVAD: true}, "")
		},
	})
	return cmd
}

func runTranscribe(cmd *cobra.Command, deps transcribeDeps, opts transcribeOptions) error {
	cmd.SilenceUsage = true
	if strings.TrimSpace(opts.request) == "" {
		return usageError("transcribe needs --request <path to an obey-transcript request>")
	}
	if !opts.jsonl {
		return usageError("transcribe needs --jsonl: JSONL is the only output form")
	}

	logf := func(format string, args ...any) {
		fmt.Fprintf(cmd.ErrOrStderr(), "transcribe: "+format+"\n", args...)
	}

	req, err := readTranscribeRequest(opts.request)
	if err != nil {
		return transcribeFailure(nil, err)
	}

	// The job ID is what makes a stream addressable, so it decides whether
	// there can be a stream at all. Without one the failure reaches the caller
	// as a diagnostic and an exit status only.
	var enc *protocol.Encoder
	if id := strings.TrimSpace(req.JobID); id != "" {
		enc = protocol.NewEncoder(cmd.OutOrStdout(), id)
		if req.Protocol == protocol.WordProtocol {
			enc = protocol.NewWordEncoder(cmd.OutOrStdout(), id)
		}
	}
	if err := req.Validate(); err != nil {
		return transcribeFailure(enc, err)
	}

	cfg, err := deps.loadConfig()
	if err != nil {
		return transcribeFailure(enc, protocol.Errorf(protocol.CodeInternal, false, "load config: %v", err))
	}

	ctx, cancel := deps.newContext()
	defer cancel()

	summary, runErr := runTranscribeJob(ctx, deps, cfg, req, enc, logf)
	if runErr != nil {
		if errors.Is(runErr, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
			return transcribeCancelled(enc, runErr)
		}
		return transcribeFailure(enc, transcribeRunError(runErr))
	}

	outcome := protocol.OutcomeSpeech
	if summary.Outcome == transcription.OutcomeNoSpeech {
		outcome = protocol.OutcomeNoSpeech
	}
	if err := enc.Encode(protocol.NewCompleted(summary.DurationUS, summary.SegmentCount, outcome)); err != nil {
		return transcribeFailure(enc, transcribeRunError(err))
	}
	return nil
}

// readTranscribeRequest reads and parses the request document.
func readTranscribeRequest(path string) (protocol.Request, error) {
	f, err := os.Open(path)
	if err != nil {
		return protocol.Request{}, protocol.Errorf(protocol.CodeInvalidRequest, false, "open request %s: %v", path, err)
	}
	defer func() { _ = f.Close() }()
	return protocol.ParseRequest(f)
}

// transcribeFailure writes the terminal failed event, when there is a stream to
// write it to, and returns the exit status its code maps to. The human message
// reaches stderr through the root error handler.
//
// A nil cause or typed-nil *protocol.Error is reported as internal, matching
// NewFailedFor. Ordinary *Error mappings are unchanged. If Encode fails while
// writing the failed event, the process status is CodeInternal/ExitRuntime so
// it matches the internal substitute on stdout, or the missing valid line.
func transcribeFailure(enc *protocol.Encoder, err error) error {
	code := protocol.CodeInternal
	message := err
	if coded, ok := errors.AsType[*protocol.Error](err); ok && coded != nil {
		code = coded.Code
		if coded.Err != nil {
			message = coded.Err
		}
	}
	if message == nil {
		message = errors.New("unspecified error")
	}
	if enc != nil && !enc.Terminal() {
		if encErr := enc.Encode(protocol.NewFailedFor(err)); encErr != nil {
			code = protocol.CodeInternal
			message = fmt.Errorf("%w (and writing the failed event: %v)", message, encErr)
		}
	}
	// The human line on stderr reads as a sentence with the wire code in
	// parentheses; the machine form is the failed event on stdout.
	return &ExitCodeError{Code: protocol.ExitFor(code), Err: fmt.Errorf("%w (%s)", message, code)}
}

// transcribeCancelled writes the terminal cancelled event and returns 130.
// Segments already emitted stay on stdout and stay partial: the missing
// completed event is what tells the client they are not a transcript.
func transcribeCancelled(enc *protocol.Encoder, cause error) error {
	if enc != nil && !enc.Terminal() {
		if encErr := enc.Encode(protocol.NewCancelled()); encErr != nil {
			return &ExitCodeError{Code: protocol.ExitCancelled, Err: encErr}
		}
	}
	if cause == nil {
		cause = context.Canceled
	}
	return &ExitCodeError{Code: protocol.ExitCancelled, Err: cause}
}

// runTranscribeJob performs the work between a valid request and a summary.
// Every failure it returns is already, or is turned into, a coded *protocol.Error.
func runTranscribeJob(
	ctx context.Context,
	deps transcribeDeps,
	cfg *config.Config,
	req protocol.Request,
	enc *protocol.Encoder,
	logf func(string, ...any),
) (transcription.Summary, error) {
	var empty transcription.Summary

	src, err := deps.openSource(req.Input.Path)
	if err != nil {
		return empty, openSourceError(req.Input.Path, err)
	}
	defer func() { _ = src.Close() }()

	if err := checkWAVFormat(req.Input, src.Format()); err != nil {
		return empty, err
	}

	norm, err := config.NormalizeSTTWithMode(cfg.STTProvider, cfg.STTMode)
	if err != nil {
		return empty, protocol.Errorf(protocol.CodeProviderUnavailable, false, "%v", err)
	}
	if err := checkTranscribeLanguage(cfg, norm, req.Language); err != nil {
		return empty, err
	}

	if req.Timing == protocol.TimingWord && (norm.Provider != config.STTProviderParakeet || (cfg.ParakeetModel != "" && cfg.ParakeetModel != "tdt-0.6b-v2") || deps.newWordDecoder == nil) {
		return empty, protocol.Errorf(protocol.CodeUnsupportedCapability, false, "configured provider does not support verbatim measured words")
	}

	// allow_model_download is false in v1, so this is a read of the filesystem
	// and never an install. A missing asset is exit 3, not a download.
	manifest, err := deps.manifest(cfg)
	if err != nil {
		return empty, protocol.Errorf(protocol.CodeProviderUnavailable, false, "resolve model assets: %v", err)
	}
	if missing := missingAssets(manifest.Status(deps.modelsDir(cfg))); len(missing) > 0 {
		return empty, protocol.Errorf(protocol.CodeModelMissing, false,
			"model assets are not installed under %s: %s; run `obey-voice models ensure`",
			deps.modelsDir(cfg), strings.Join(missing, ", "))
	}

	// Every shipped backend rejects a nil VAD, and this path needs spans
	// whatever the conversational setting says. The override lives on a copy:
	// nothing here writes config.
	sttCfg := *cfg
	sttCfg.VADEnabled = true

	build := deps.newDecoder
	if req.Timing == protocol.TimingWord {
		build = deps.newWordDecoder
	}
	decoder, info, closeDecoder, err := build(&sttCfg)
	if err != nil {
		return empty, decoderError(err)
	}
	if closeDecoder != nil {
		defer closeDecoder()
	}

	var detector transcription.SpanDetector
	if req.Timing == protocol.TimingWord {
		detector = transcription.NewWindowSpanDetector(audio.SampleRate)
	} else {
		var closeDetector func()
		detector, closeDetector, err = deps.newDetector(&sttCfg)
		if err != nil {
			return empty, protocol.Errorf(protocol.CodeProviderUnavailable, false, "init file VAD: %v", err)
		}
		if closeDetector != nil {
			defer closeDetector()
		}
	}

	language := config.LanguageCode(cfg.Language)
	if req.Language != "" {
		language = config.LanguageCode(req.Language)
	}
	sink := &protocolSink{enc: enc, timing: req.Timing, language: language}
	minSpeech, floor := 0, 0.0
	if req.Timing == protocol.TimingWord {
		// Do not silently discard short fillers or quiet detected speech.
		minSpeech, floor = 1, math.SmallestNonzeroFloat64
	}
	return deps.run(ctx, transcription.Request{
		JobID:    req.JobID,
		Language: req.Language,
		Timing:   req.Timing,
	}, transcription.Deps{
		MinSpeechSamples: minSpeech, SpeechFloor: floor,
		Source:   src,
		Detector: detector,
		Decoder:  decoder,
		Info:     info,
		Sink:     sink,
		Logf:     logf,
	})
}

// protocolSink turns runner events into wire events. It is called from the
// runner's goroutine and writes synchronously, so a slow consumer of stdout
// backpressures the read loop instead of dropping audio.
type protocolSink struct {
	enc      *protocol.Encoder
	timing   string
	language string
}

func (s *protocolSink) Emit(ctx context.Context, ev transcription.Event) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	switch e := ev.(type) {
	case transcription.Accepted:
		a := protocol.NewAccepted(e.Provider, e.ModelID, e.ModelRevision, s.timing)
		if s.timing == protocol.TimingWord {
			a.TextPolicy, a.TimingMethod, a.TimingValidity = "verbatim", stt.WordTimingMethod, "estimated"
			a.Language, a.Runtime = s.language, "sherpa-onnx/1.13.8"
		}
		return s.enc.Encode(a)
	case transcription.Progress:
		return s.enc.Encode(protocol.NewProgress(e.ConsumedUS, e.DurationUS))
	case transcription.Segment:
		segment := protocol.NewSegment(e.ID, e.StartUS, e.EndUS, e.Text, s.timing)
		for i, word := range e.Words {
			segment.Words = append(segment.Words, protocol.Word{ID: fmt.Sprintf("%s-w%d", e.ID, i), Text: word.Text, StartUS: word.StartUS, EndUS: word.EndUS})
		}
		return s.enc.Encode(segment)
	default:
		return nil
	}
}

// openSourceError classifies why the input could not be opened. A file that is
// absent or unreadable is one failure; anything else the source refused is a
// disagreement between the file and the request.
func openSourceError(path string, err error) error {
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, fs.ErrPermission) {
		return protocol.Errorf(protocol.CodeInputUnreadable, false, "%v", err)
	}
	return protocol.Errorf(protocol.CodeInputFormatMismatch, false, "%s: %v", path, err)
}

// checkWAVFormat compares the file's actual header with what the request
// declared. The request is the caller's claim; the header is the fact.
func checkWAVFormat(in protocol.Input, got audio.WAVFormat) error {
	switch {
	case got.SampleRate != in.SampleRateHz:
		return protocol.Errorf(protocol.CodeInputFormatMismatch, false,
			"%s is %d Hz, the request declares %d Hz", in.Path, got.SampleRate, in.SampleRateHz)
	case got.Channels != in.Channels:
		return protocol.Errorf(protocol.CodeInputFormatMismatch, false,
			"%s has %d channel(s), the request declares %d", in.Path, got.Channels, in.Channels)
	case got.BitsPerSample != 16:
		return protocol.Errorf(protocol.CodeInputFormatMismatch, false,
			"%s is %d-bit, %s is 16-bit", in.Path, got.BitsPerSample, protocol.FormatWAVPCMS16LE)
	}
	return nil
}

func missingAssets(statuses []config.AssetStatus) []string {
	var missing []string
	for _, st := range statuses {
		if !st.Installed {
			missing = append(missing, st.Name)
		}
	}
	return missing
}

// decoderError maps a decoder construction failure. A provider that cannot
// decode a finite span is an installation choice, not a bad request, so it is
// exit 3 like any other unavailable provider.
func decoderError(err error) error {
	if errors.Is(err, stt.ErrSegmentDecodeUnsupported) {
		return protocol.Errorf(protocol.CodeProviderUnavailable, false,
			"the configured stt_provider cannot transcribe a file: %v", err)
	}
	return protocol.Errorf(protocol.CodeProviderUnavailable, false, "init STT: %v", err)
}

// transcribeRunError gives a runner failure a wire code. A runner error that
// already carries one of the stable codes keeps it; anything else is a decode
// failure, which is the honest default for a failure inside recognition.
func transcribeRunError(err error) error {
	if coded, ok := errors.AsType[*protocol.Error](err); ok && coded != nil {
		return coded
	}
	if runnerErr, ok := errors.AsType[*transcription.Error](err); ok && runnerErr != nil {
		code := runnerErr.Code
		if !protocol.KnownCode(code) {
			code = protocol.CodeDecodeFailed
		}
		return &protocol.Error{Code: code, Retryable: runnerErr.Retryable, Err: runnerErr.Err}
	}
	return protocol.Errorf(protocol.CodeDecodeFailed, true, "%v", err)
}

// checkTranscribeLanguage rejects a language the configured model is known not
// to transcribe. It reads the asset tables only, so it runs before anything is
// constructed and its failure is exit 2.
func checkTranscribeLanguage(cfg *config.Config, norm config.NormalizedSTT, language string) error {
	if strings.TrimSpace(language) == "" {
		return nil
	}
	langs, auto := transcribeLanguages(cfg, norm)
	if auto || len(langs) == 0 {
		return nil
	}
	if slices.Contains(langs, config.LanguageCode(language)) {
		return nil
	}
	return protocol.Errorf(protocol.CodeUnsupportedCapability, false,
		"language %q is not available: the configured model transcribes %s", language, strings.Join(langs, ", "))
}

// transcribeLanguages reports the language codes the model resolved for norm is
// known to transcribe, and whether the provider accepts languages this build
// does not enumerate. It reads the asset tables only: nothing is constructed
// and no file is opened.
func transcribeLanguages(cfg *config.Config, norm config.NormalizedSTT) (langs []string, autoDetect bool) {
	english := []string{"en"}
	switch norm.Provider {
	case config.STTProviderParakeet:
		asset, err := config.ParakeetModel(cfg.ParakeetModel)
		if err == nil && asset.EnglishOnly() {
			return english, false
		}
	case config.STTProviderTranscribeCPP:
		asset, err := config.TranscribeCPPModel(cfg.TranscribeCPPModel)
		if err == nil && strings.EqualFold(strings.TrimSpace(asset.Languages), "English") {
			return english, false
		}
	case config.STTProviderWhisperCPP:
		asset, err := config.WhisperCPPModelAsset(cfg.WhisperCPPModel)
		if err == nil && strings.HasSuffix(strings.TrimSuffix(asset.Filename, ".bin"), ".en") {
			return english, false
		}
	case config.STTProviderSherpa:
		if norm.Mode == config.STTModeStreaming {
			if code, _, ok := strings.Cut(strings.TrimSpace(cfg.SherpaStreamingModel), "-"); ok && len(code) == 2 {
				return []string{strings.ToLower(code)}, false
			}
			break
		}
		model, err := config.SherpaOfflineWhisperModel(cfg.WhisperModel)
		if err == nil && strings.HasSuffix(model, ".en") {
			return english, false
		}
	}
	return nil, true
}
