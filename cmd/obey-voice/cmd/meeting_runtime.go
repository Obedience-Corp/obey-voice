//go:build !integration

package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/mattn/go-isatty"
	"github.com/spf13/cobra"

	"github.com/Obedience-Corp/obey-voice/internal/listen"
	"github.com/Obedience-Corp/obey-voice/internal/meeting"
	meetinglog "github.com/Obedience-Corp/obey-voice/internal/meeting/log"
	"github.com/Obedience-Corp/obey-voice/internal/speaker"
	appTUI "github.com/Obedience-Corp/obey-voice/internal/tui"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/stt"
)

// runMeetingRecord wires the STT-only chain into listen.Loop with the file
// writer and console/JSON sinks. Nothing is written to disk until the STT
// stack has constructed successfully. On an interactive TTY (and not
// --json / --no-tui) the Bubble Tea meeting UI shows the same voice EQ as
// the conversation screen.
func runMeetingRecord(cmd *cobra.Command, opts meetingOptions) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	// One-shot provider override: clear stt_mode exactly as benchmark.go does
	// so a compound alias never conflicts with a leftover configured mode.
	cfgCopy := *cfg
	if opts.STTProvider != "" {
		cfgCopy.STTProvider = opts.STTProvider
		cfgCopy.STTMode = ""
	}

	ctx, cancel := signalContext()
	defer cancel()

	outDir := opts.OutDir
	if outDir == "" {
		outDir = config.MeetingsDir()
	}
	// 0o700: meeting filenames include description slugs; keep the directory
	// private the same way auth/cert paths are.
	if err := os.MkdirAll(outDir, 0o700); err != nil {
		return fmt.Errorf("meeting record: create out dir: %w", err)
	}
	// Resolve --resume first: a typo must not cost the user a full model
	// preflight and a hot microphone before it is reported.
	resume, err := resolveMeetingResume(ctx, outDir, opts.Resume)
	if err != nil {
		return err
	}

	progress := meetingAssetProgress(opts.JSON)
	captureLogf, closeCaptureLog := meetingCaptureLogf(useMeetingRecordTUI(opts))
	defer closeCaptureLog()
	capture, provider, sttLabel, sttCleanup, err := buildSTTOnly(ctx, &cfgCopy, progress, captureLogf)
	if err != nil {
		return err
	}
	// Two-stage teardown (launcher parity, WI-162bbb R1): releaseCapture frees
	// the mic/STT/live stack at capture stop — the device must not stay hot
	// through review and diarization — while the analyzer stays alive for
	// background Finalize; the deferred speakerSession.Close releases it.
	var speakerSession *meeting.SpeakerSession
	var stopLive func()
	var releaseOnce sync.Once
	releaseCapture := func() {
		releaseOnce.Do(func() {
			if stopLive != nil {
				stopLive()
			}
			if speakerSession != nil {
				speakerSession.StopCapture()
			}
			sttCleanup()
		})
	}
	defer releaseCapture()

	writer, bundlePath, err := openMeetingWriter(outDir, opts.Description, sttLabel, resume)
	if err != nil {
		return err
	}
	alreadyRouted := resume != nil && resume.alreadyRouted
	opts.Description = writer.Description()
	// Durable delivery intent: an auto destination known at start is recorded
	// so the launcher's startup sweep can retry if this process never routes.
	// A failed write voids that promise — say so instead of claiming durability.
	// A resume keeps the plan the bundle already carries; a resume of a bundle
	// with no plan writes one, so --resume last --route docs is as crash-proof
	// as a fresh recording.
	if dest := cliAutoRouteDest(meeting.FromConfig(&cfgCopy), opts); dest != "" &&
		(resume == nil || !resume.hasRoutePlan) {
		mode := "auto"
		if opts.RouteTo != "" {
			mode = "dest"
		}
		if err := writer.WriteRoutePlan(dest, mode); err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(),
				"warning: route plan not persisted: %v — %s delivery will not survive a crash\n", err, dest)
		}
	}
	speakerSession, speakerSetupErr := prepareMeetingSpeakers(ctx, &cfgCopy, capture, writer, bundlePath, progress)
	if speakerSession != nil {
		defer speakerSession.Close()
	}
	if speakerSetupErr != nil {
		_ = writer.WriteSpeakerAnalysis(meetinglog.SpeakerAnalysis{
			Status: string(meeting.AnalysisError), Error: speakerSetupErr.Error(),
		})
	}
	var liveSpeaker appTUI.LiveSpeakerController
	liveSpeaker, stopLive = prepareMeetingLiveSpeaker(ctx, &cfgCopy, capture)

	out := cmd.OutOrStdout()
	useTUI := useMeetingRecordTUI(opts)

	var loopErr error
	if useTUI {
		loopErr = appTUI.RunMeeting(appTUI.MeetingOpts{
			Ctx:           ctx,
			Cancel:        cancel,
			Capture:       capture,
			Provider:      provider,
			Writer:        writer,
			Description:   opts.Description,
			Path:          bundlePath,
			StopPhrases:   stopPhraseSet(opts.StopPhrases),
			SpeakerStatus: speakerInitialStatus(speakerSession, speakerSetupErr),
			SpeakerError:  speakerInitialDetail(speakerSession, speakerSetupErr),
			LiveSpeaker:   liveSpeaker,
		})
	} else {
		if liveSpeaker != nil {
			liveSpeaker.SetEnabled(true)
		}
		var sinks []listen.Sink
		sinks = append(sinks, writer)
		if opts.JSON {
			sinks = append(sinks, &jsonSink{enc: json.NewEncoder(out)})
		} else {
			fmt.Fprintf(out, "Recording meeting: %q\n", opts.Description)
			fmt.Fprintf(out, "Meeting bundle: %s\n", bundlePath)
			fmt.Fprintln(out, "🎙 Listening... (say \"stop recording\" or press Ctrl+C to stop)")
			sinks = append(sinks, &consoleSink{out: out, errOut: cmd.ErrOrStderr()})
		}
		sink := &stopPhraseSink{
			inner:   multiSink(sinks),
			phrases: stopPhraseSet(opts.StopPhrases),
			stop:    cancel,
		}
		loopErr = listen.Loop(ctx, capture, provider, sink)
	}

	// Capture ended: free the mic/STT/live stack now — the device must not
	// stay hot through review or diarization, and unsubscribing the speaker
	// session here keeps post-stop PCM out of the diarization working file.
	releaseCapture()
	// Trailer/session_end land now. Diarization and routing run behind the
	// review instead of gating it (WI-162bbb R1/R2); analysis events append
	// to the closed bundle.
	summary, closeErr := writer.Close()

	resultsAnalysis, waitAnalysis, stopAnalysis := startBackgroundDiarize(speakerSession)

	// Auto-chosen destinations (--route, mode=auto default) deliver at capture
	// end, before review; ask-mode still prompts after review.
	var routeErr error
	autoDest := ""
	routeCfg, cfgErr := config.Load()
	if cfgErr != nil {
		routeErr = cfgErr
	} else {
		autoDest = cliAutoRouteDest(meeting.FromConfig(routeCfg), opts)
	}
	if closeErr == nil && cfgErr == nil && autoDest != "" && !(alreadyRouted && opts.RouteTo == "") {
		routeErr = routeAfterRecordTo(cmd, routeCfg, summary, autoDest, opts.JSON)
	}

	if useTUI && closeErr == nil {
		if err := appTUI.RunMeetingResults(summary, resultsAnalysis); err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "meeting review: %v\n", err)
		}
	}
	if closeErr == nil && cfgErr == nil && autoDest == "" && !alreadyRouted {
		routeErr = maybeRouteAfterRecord(cmd, routeCfg, summary, opts)
	}

	speakerResult, speakerRan := awaitDiarize(cmd, waitAnalysis, stopAnalysis)
	if speakerRan {
		foldSpeakerOutcome(&summary, speakerResult)
	}

	var outputErr error
	if opts.JSON {
		outputErr = json.NewEncoder(out).Encode(summary)
	} else {
		fmt.Fprintln(out)
		fmt.Fprintln(out, "Meeting recording stopped.")
		fmt.Fprintf(out, "  Description: %s\n", summary.Description)
		fmt.Fprintf(out, "  Bundle:      %s\n", summary.Bundle)
		fmt.Fprintf(out, "  Meeting:     %s\n", summary.File)
		fmt.Fprintf(out, "  Duration:    %s\n", summary.Duration().Round(time.Second))
		fmt.Fprintf(out, "  Utterances:  %d\n", summary.Utterances)
		fmt.Fprintf(out, "  Notes:       %d\n", summary.Notes)
		fmt.Fprintf(out, "  Bookmarks:   %d\n", summary.Bookmarks)
		if speakerSetupErr != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "  Speaker analysis unavailable: %v (recording preserved)\n", speakerSetupErr)
		} else if speakerResult.Status != "" {
			fmt.Fprintf(out, "  Speakers:    %s\n", meetingAnalysisSummary(speakerResult))
		}
	}
	return errors.Join(loopErr, closeErr, outputErr, routeErr)
}

// startBackgroundDiarize finalizes speaker analysis off the main flow. It
// returns one outcome channel for the live review fold, one for the summary
// wait (each buffered so neither reader can starve the other), and a cancel
// that releases the skip-signal watcher.
func startBackgroundDiarize(session *meeting.SpeakerSession) (<-chan appTUI.MeetingAnalysisOutcome, <-chan appTUI.MeetingAnalysisOutcome, context.CancelFunc) {
	if session == nil {
		return nil, nil, nil
	}
	// A fresh signal context makes Ctrl-C skip enrichment cleanly instead of
	// being swallowed by the recording's already-fired handler.
	sctx, scancel := signalContext()
	actx, acancel := context.WithTimeout(sctx, 2*time.Hour)
	cancel := func() {
		acancel()
		scancel()
	}
	resultsCh := make(chan appTUI.MeetingAnalysisOutcome, 1)
	waitCh := make(chan appTUI.MeetingAnalysisOutcome, 1)
	go func() {
		result, err := session.Finalize(actx)
		outcome := appTUI.MeetingAnalysisOutcome{Result: result, Err: err}
		resultsCh <- outcome
		close(resultsCh)
		waitCh <- outcome
		close(waitCh)
	}()
	return resultsCh, waitCh, cancel
}

// awaitDiarize blocks for background analysis, telling an interactive user
// how to skip. Ctrl-C cancels enrichment only — the transcript and any
// delivered route are already on disk.
func awaitDiarize(cmd *cobra.Command, wait <-chan appTUI.MeetingAnalysisOutcome, cancel context.CancelFunc) (meeting.AnalysisResult, bool) {
	if wait == nil {
		return meeting.AnalysisResult{}, false
	}
	defer cancel()
	select {
	case outcome := <-wait:
		return outcome.Result, true
	default:
	}
	fmt.Fprintln(cmd.ErrOrStderr(), "Diarizing speakers… (Ctrl+C skips speaker labels; transcript is saved)")
	outcome := <-wait
	return outcome.Result, true
}

// foldSpeakerOutcome mirrors the bundle's post-close analysis into the
// summary so console/JSON output reports what diarization produced.
func foldSpeakerOutcome(summary *meetinglog.Summary, result meeting.AnalysisResult) {
	summary.SpeakerStatus = string(result.Status)
	summary.SpeakerCount = result.SpeakerCount
	summary.SpeakerAnalysisFile = result.Artifact
	summary.AudioFile = result.AudioFile
	summary.SpeakerError = result.Error
}

// meetingCaptureLogf picks where capture reopen diagnostics go on the
// standalone `meeting record` path. That path runs its own Bubble Tea program
// without redirecting file descriptor 2, so a line on stderr would be painted
// over the recorder; those go to the same native-diagnostics log the launcher
// uses. Without the TUI, stderr is free and log.Printf is the repo default. A
// log file that cannot be opened yields a silent capture rather than a
// corrupted screen.
func meetingCaptureLogf(useTUI bool) (func(string, ...any), func()) {
	if !useTUI {
		return log.Printf, func() {}
	}
	path := appTUI.DiagnosticsLogPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, func() {}
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, func() {}
	}
	logger := log.New(file, "", log.LstdFlags)
	return logger.Printf, func() { _ = file.Close() }
}

// useMeetingRecordTUI is true for an interactive terminal session that should
// open the Bubble Tea recorder. --json and --no-tui keep the plain sinks so
// scripts never hang on a full-screen UI.
func useMeetingRecordTUI(opts meetingOptions) bool {
	if opts.JSON || opts.NoTUI {
		return false
	}
	return isatty.IsTerminal(os.Stdout.Fd()) && isatty.IsTerminal(os.Stdin.Fd())
}

// resumeTarget is a validated --resume reference: the bundle to append to,
// plus what its event stream already promises about routing.
type resumeTarget struct {
	bundle        string
	alreadyRouted bool
	hasRoutePlan  bool
}

// resolveMeetingResume validates a --resume reference without touching models
// or the microphone. A nil target with a nil error means no --resume was given.
func resolveMeetingResume(ctx context.Context, outDir, ref string) (*resumeTarget, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil, nil
	}
	entry, ok := meeting.ResolveResumeRef(ctx, outDir, ref)
	if !ok {
		return nil, fmt.Errorf("meeting record: no meeting matching %q", ref)
	}
	return &resumeTarget{
		bundle:        entry.Bundle,
		alreadyRouted: entry.Route != nil && entry.Route.Status == meeting.RouteStatusRouted,
		hasRoutePlan:  entry.Route != nil && entry.Route.Status != meeting.RouteStatusNone,
	}, nil
}

// openMeetingWriter creates a new bundle, or reopens the already-resolved
// --resume target.
func openMeetingWriter(outDir, description, sttLabel string, resume *resumeTarget) (*meetinglog.Writer, string, error) {
	if resume != nil {
		writer, err := meetinglog.OpenBundle(resume.bundle)
		if err != nil {
			return nil, "", err
		}
		return writer, resume.bundle, nil
	}
	bundlePath := filepath.Join(outDir, meetingBundleName(description, time.Now()))
	writer, err := meetinglog.CreateBundle(bundlePath, description, sttLabel, "mac")
	if err != nil {
		return nil, "", err
	}
	return writer, bundlePath, nil
}

// meetingRuntimeBuilder powers the main launcher "Meeting" entry.
func meetingRuntimeBuilder() appTUI.MeetingBuilder {
	return func(ctx context.Context, spec appTUI.MeetingSpec, progress appTUI.BuildProgress) (*appTUI.MeetingRuntime, error) {
		progress.Stage("Reading settings")
		cfg, err := config.Load()
		if err != nil {
			return nil, err
		}
		outDir := config.MeetingsDir()
		if err := os.MkdirAll(outDir, 0o700); err != nil {
			return nil, fmt.Errorf("meeting: create out dir: %w", err)
		}
		description := spec.Description

		// VHS/demo path: skip real mic/models; the TUI scripts STT events.
		if os.Getenv("OBEY_VOICE_DEMO_MEETING") == "1" ||
			os.Getenv("OBEY_VOICE_DEMO_MEETING") == "true" ||
			os.Getenv("OBEY_VOICE_DEMO_MEETING") == "yes" {
			bundlePath := filepath.Join(outDir, meetingBundleName(description, time.Now()))
			writer, err := meetinglog.CreateBundle(bundlePath, description, "demo", "mac")
			if err != nil {
				return nil, err
			}
			return &appTUI.MeetingRuntime{
				// Non-nil placeholders; meeting loop swaps in demo provider when
				// OBEY_VOICE_DEMO_MEETING is set.
				Capture:     noopResetter{},
				Provider:    noopProvider{},
				Writer:      writer,
				Description: description,
				Path:        bundlePath,
				StopPhrases: stopPhraseSet(nil),
				Cleanup:     func() {},
			}, nil
		}

		resume, err := resolveMeetingResume(ctx, outDir, spec.ResumePath)
		if err != nil {
			return nil, err
		}

		// The launcher redirects file descriptor 2 into the diagnostics log for
		// the life of the program, so the standard logger is already off the
		// terminal here.
		capture, provider, sttLabel, cleanup, err := buildSTTOnly(ctx, cfg, progress, log.Printf)
		if err != nil {
			return nil, err
		}
		progress.Stage("Opening the meeting bundle")
		writer, bundlePath, err := openMeetingWriter(outDir, description, sttLabel, resume)
		if err != nil {
			cleanup()
			return nil, err
		}
		description = writer.Description()
		speakerSession, speakerSetupErr := prepareMeetingSpeakers(ctx, cfg, capture, writer, bundlePath, progress)
		if speakerSetupErr != nil {
			_ = writer.WriteSpeakerAnalysis(meetinglog.SpeakerAnalysis{
				Status: string(meeting.AnalysisError), Error: speakerSetupErr.Error(),
			})
		}
		liveSpeaker, stopLive := prepareMeetingLiveSpeaker(ctx, cfg, capture)
		runtimeCleanup := cleanup
		// Two-stage teardown: capture resources (mic, STT, live labels) release
		// at stop so the next screen can use the device, while the speaker
		// analyzer stays alive for background diarization. Each stage is
		// idempotent so the global-quit path may run both unconditionally.
		var releaseOnce, cleanupOnce sync.Once
		releaseCapture := func() {
			releaseOnce.Do(func() {
				if stopLive != nil {
					stopLive()
				}
				if speakerSession != nil {
					speakerSession.StopCapture()
				}
				runtimeCleanup()
			})
		}
		cleanup = func() {
			releaseCapture()
			cleanupOnce.Do(func() {
				if speakerSession != nil {
					_ = speakerSession.Close()
				}
			})
		}
		return &appTUI.MeetingRuntime{
			Capture:          capture,
			Provider:         provider,
			Writer:           writer,
			FinalizeSpeakers: speakerFinalizer(speakerSession),
			SpeakerStatus:    speakerInitialStatus(speakerSession, speakerSetupErr),
			SpeakerError:     speakerInitialDetail(speakerSession, speakerSetupErr),
			LiveSpeaker:      liveSpeaker,
			Description:      description,
			Path:             bundlePath,
			StopPhrases:      stopPhraseSet(nil),
			ReleaseCapture:   releaseCapture,
			Cleanup:          cleanup,
		}, nil
	}
}

// prepareMeetingLiveSpeaker builds the same LazyLive stack as chat when
// speaker.meeting.live is on. Capture is shared with STT (fan-out); live feed
// never blocks the listen loop.
func prepareMeetingLiveSpeaker(
	ctx context.Context,
	cfg *config.Config,
	capture *audio.Capture,
) (appTUI.LiveSpeakerController, func()) {
	sp := speaker.FromAppConfig(cfg)
	if !sp.MeetingLiveActive() || capture == nil {
		return nil, nil
	}
	// Reuse chat's live builder so meeting labels share the embedding model
	// and /speakers-class startup path (assets ensured on first enable).
	// Do not SetEnabled here: TitaNet load pins a handheld long enough that
	// the waiting screen freezes on "Preparing speaker identification".
	// beginRecording / CLI Init enable once the recorder is on screen.
	controller, stop, _ := prepareLiveSpeaker(ctx, cfg, capture)
	return controller, stop
}

func prepareMeetingSpeakers(ctx context.Context, cfg *config.Config, capture *audio.Capture, writer *meetinglog.Writer, bundlePath string, progress appTUI.BuildProgress) (*meeting.SpeakerSession, error) {
	sp := speaker.FromAppConfig(cfg)
	if !sp.MeetingActive() {
		return nil, nil
	}
	// Download weights here (progress ticks). Do not load pyannote/TitaNet on
	// this goroutine: NewSherpaEngine can sit for minutes on a handheld, and
	// live TitaNet is started after the recorder screen is up.
	progress.Stage("Checking speaker models")
	if err := config.EnsureRuntimeAssets(ctx, cfg, config.AssetRequest{NeedSpeaker: true}, progress.Downloading); err != nil {
		return nil, fmt.Errorf("prepare speaker models: %w", err)
	}
	// Everything that can be known before capture must fail here, not after a
	// recording the user cannot re-run: an unknown speaker.meeting.diarizer, a
	// Sortformer selection without its CLI or GGUF, and a segmentation or
	// embedding file that is missing at its configured path. The diarizer value
	// is kept so the deferred builder does not probe the CLI a second time.
	diarizer, err := meetingDiarizer(ctx, cfg)
	if err != nil {
		return nil, err
	}
	modelsDir := config.ModelsDirFrom(cfg)
	// Paths only; the native graphs stay unloaded until Finalize.
	if err := speaker.RequireSherpaModels(sp, modelsDir); err != nil {
		return nil, err
	}
	// Live labels already hold one TitaNet session while recording. Offline
	// pyannote + the review engine's embedding extractor wait until Finalize
	// so a long take does not keep a second ONNX stack resident. The deferral
	// is also what keeps the load off this goroutine, so a session — new or
	// resumed — needs no background preload here.
	analyzer := meeting.NewDeferredAnalyzer(func(context.Context) (meeting.SpeakerAnalyzer, error) {
		engine, err := speaker.NewSherpaEngine(sp, modelsDir)
		if err != nil {
			return nil, err
		}
		seedEnrolledProfiles(cfg, engine, nil)
		built, err := meetingAnalyzerWith(sp, engine, diarizer)
		if err != nil {
			_ = engine.Close()
			return nil, err
		}
		return built, nil
	})
	session, err := meeting.NewSpeakerSession(capture, analyzer, writer, bundlePath, sp.Meeting.RecordAudio)
	if err != nil {
		_ = analyzer.Close()
		return nil, err
	}
	return session, nil
}

func speakerFinalizer(session *meeting.SpeakerSession) func(context.Context) (meeting.AnalysisResult, error) {
	if session == nil {
		return nil
	}
	return session.Finalize
}

func speakerInitialStatus(session *meeting.SpeakerSession, setupErr error) meeting.AnalysisStatus {
	if setupErr != nil {
		return meeting.AnalysisError
	}
	if session != nil {
		return meeting.AnalysisQueued
	}
	return meeting.AnalysisDisabled
}

func speakerInitialDetail(session *meeting.SpeakerSession, setupErr error) string {
	if setupErr != nil {
		return setupErr.Error() + " (recording unaffected)"
	}
	if session != nil {
		return "collecting audio; diarizes when recording stops"
	}
	return ""
}

func meetingAnalysisSummary(result meeting.AnalysisResult) string {
	if result.Error != "" {
		return string(result.Status) + " — " + result.Error
	}
	return fmt.Sprintf("%s — %d detected · %s", result.Status, result.SpeakerCount, result.Artifact)
}

// noopResetter/provider satisfy the MeetingRuntime fields for demo builds.
// The meeting TUI replaces them when OBEY_VOICE_DEMO_MEETING is set.
type noopResetter struct{}

func (noopResetter) Reset() {}

type noopProvider struct{}

func (noopProvider) Available() bool { return true }
func (noopProvider) Start(ctx context.Context) (stt.Session, error) {
	return nil, fmt.Errorf("demo noop provider")
}

func meetingAssetProgress(jsonOutput bool) appTUI.BuildProgress {
	if jsonOutput {
		// Machine-readable output must remain JSONL even on a first run that
		// downloads model assets. modelProgress writes human text to stdout.
		return nil
	}
	return func(stage appTUI.BuildStage) {
		if stage.Download {
			modelProgress(strings.TrimPrefix(stage.Label, "Downloading "), stage.Pct)
			return
		}
		printStage(stage.Label)
	}
}

// buildSTTOnly constructs asset preflight -> capture -> VAD -> STT provider,
// with no Brain or TTS — the chain benchmark.go's runSingleSTTBenchmark
// proves. VAD is required: every shipped STT backend rejects a nil VAD.
// root.go's buildPipeline should eventually share this helper.
// captureLogf is where the capture device sends reopen diagnostics; nil is
// silent. It is a parameter because the right destination depends on who owns
// the terminal, which this helper cannot see.
func buildSTTOnly(ctx context.Context, cfg *config.Config, progress appTUI.BuildProgress, captureLogf func(string, ...any)) (capture *audio.Capture, provider stt.Provider, sttLabel string, cleanup func(), err error) {
	progress.Stage("Checking models")
	if err := config.EnsureRuntimeAssets(ctx, cfg, config.AssetRequest{NeedSTT: true, NeedVAD: true}, progress.Downloading); err != nil {
		return nil, nil, "", nil, err
	}

	var cleanups []func()
	cleanup = func() {
		for i := len(cleanups) - 1; i >= 0; i-- {
			cleanups[i]()
		}
	}
	fail := func(e error) (*audio.Capture, stt.Provider, string, func(), error) {
		cleanup()
		return nil, nil, "", nil, e
	}

	capture = audio.NewCaptureWithDevice(cfg.InputDevice)
	capture.Logf = captureLogf
	// AGC only — no noise suppressor. NS over-gates far/quiet talkers, which is
	// the opposite of what meeting notes need. Conversation barge-in keeps the
	// full voice front-end; this path is STT-only.
	gain := audio.NewListenGain()
	capture.SetFrontend(gain)
	cleanups = append(cleanups, func() { _ = gain.Close() })
	if err := capture.Start(ctx); err != nil {
		return fail(fmt.Errorf("start capture: %w", err))
	}
	cleanups = append(cleanups, capture.Stop)

	vad, err := audio.NewVAD(cfg)
	if err != nil {
		return fail(fmt.Errorf("init VAD: %w", err))
	}
	cleanups = append(cleanups, vad.Delete)

	provider, sttCleanup, err := stt.NewProvider(cfg, capture, vad)
	if err != nil {
		return fail(fmt.Errorf("init STT: %w", err))
	}
	if sttCleanup != nil {
		cleanups = append(cleanups, sttCleanup)
	}

	label := cfg.STTProvider
	if norm, nerr := config.NormalizeSTTWithMode(cfg.STTProvider, cfg.STTMode); nerr == nil {
		label = fmt.Sprintf("%s (%s)", norm.Provider, norm.Mode)
	}
	return capture, provider, label, cleanup, nil
}

// multiSink fans one event out to every sink.
type multiSink []listen.Sink

func (m multiSink) OnUtterance(u listen.Utterance) error {
	for _, s := range m {
		if err := s.OnUtterance(u); err != nil {
			return err
		}
	}
	return nil
}
func (m multiSink) OnTimeout() error {
	for _, s := range m {
		if err := s.OnTimeout(); err != nil {
			return err
		}
	}
	return nil
}
func (m multiSink) OnError(err error) error {
	for _, s := range m {
		if sinkErr := s.OnError(err); sinkErr != nil {
			return sinkErr
		}
	}
	return nil
}

// stopPhraseSink intercepts stop phrases before they reach the log: a match
// ends the recording (same path as Ctrl+C) and is intentionally not written
// as content (operators should not see the stop command in the transcript).
type stopPhraseSink struct {
	inner   listen.Sink
	phrases map[string]bool
	stop    func()
}

func (s *stopPhraseSink) OnUtterance(u listen.Utterance) error {
	if s.phrases[meeting.NormalizeStopPhrase(u.Text)] {
		s.stop()
		return nil
	}
	return s.inner.OnUtterance(u)
}
func (s *stopPhraseSink) OnTimeout() error        { return s.inner.OnTimeout() }
func (s *stopPhraseSink) OnError(err error) error { return s.inner.OnError(err) }

// consoleSink echoes utterances to stdout and errors to stderr.
type consoleSink struct {
	out    io.Writer
	errOut io.Writer
}

func (c *consoleSink) OnUtterance(u listen.Utterance) error {
	_, err := fmt.Fprintf(c.out, "[%s] %s\n", u.At.Format("15:04:05"), u.Text)
	return err
}
func (c *consoleSink) OnTimeout() error { return nil }
func (c *consoleSink) OnError(err error) error {
	_, writeErr := fmt.Fprintf(c.errOut, "  transcription error: %v (retrying)\n", err)
	return writeErr
}

// jsonSink emits one JSON line per utterance for live scripting.
type jsonSink struct{ enc *json.Encoder }

func (j *jsonSink) OnUtterance(u listen.Utterance) error {
	return j.enc.Encode(struct {
		TS   string `json:"ts"`
		Text string `json:"text"`
	}{TS: u.At.Format(time.RFC3339), Text: u.Text})
}
func (j *jsonSink) OnTimeout() error { return nil }
func (j *jsonSink) OnError(_ error) error {
	return nil
}
