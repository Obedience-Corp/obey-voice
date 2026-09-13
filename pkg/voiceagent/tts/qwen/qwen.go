package qwen

import (
	"context"
	"errors"
	"fmt"
	tts "github.com/Obedience-Corp/obey-voice/pkg/voiceagent/tts"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	managedqwen "github.com/Obedience-Corp/obey-voice/pkg/voiceagent/qwen"
)

const (
	qwen3TTSProviderName  = "qwen3-tts"
	qwen3TTSSampleRate    = 24000
	defaultQwenTTSTimeout = 120 * time.Second
	maxQwenAudioDuration  = 2 * time.Hour
	maxWorkerOutput       = 8 << 10

	// Cold ready (~6s measured, 11s at 1.7b) is paid once at session start,
	// not every turn. It is added only while the worker is not yet alive.
	nativeColdReadyP95 = 12 * time.Second
	// Managed Python whole-utterance was ~6–9s process wall; keep prior default floor.
	managedWarmTTP95 = 15 * time.Second

	// nativeRTFCeiling is the realtime factor the FIRST segment of a turn is
	// expected to stay under, per tier, with margin. The first segment is the
	// only one this budget is ever computed for, and it is measurably the
	// expensive one.
	//
	// Measured in-pipeline on an M4 Max, 2026-08-27, with a 27B brain
	// resident. At 1.7b the first segment of a turn ran 3.48 and 3.11 while
	// every later segment in the same replies ran 2.15–2.61 — a fixed
	// first-request cost that persists even on an already-warm worker, and
	// which reproduced across separate runs. Sizing this from the median
	// (2.41) would under-budget every turn's opening.
	//
	// F7's isolated lab run measured 1.87 median for this tier, so the
	// product pays roughly 29% more than the bench does; these come from the
	// in-pipeline figures, not the lab one.
	//
	// They err high on purpose. A budget shorter than healthy synthesis cuts
	// off a working reply mid-sentence; one that is too long only makes a
	// broken turn take longer to give up. The asymmetry is not close.
	nativeRTFCeiling06B = 3.0
	nativeRTFCeiling17B = 4.0
	// nativeRTFCeilingCPU is the Pocket 4 / linux-amd64 native package.
	// Isolated 0.6b on this host measured ~14.6× realtime (TTFA 13.5s for a
	// short "Hello there."). The 3.0 Metal/CUDA ceiling expires at ~30s on a
	// 60-char opener and Kokoro takes the rest of the reply.
	nativeRTFCeilingCPU = 16.0
	// minFirstAudioGraceCPU floors CPU native so a 13.5s short still has
	// margin after a warm worker; 8s is the Metal/CUDA scheduler floor.
	minFirstAudioGraceCPU = 24 * time.Second

	// minFirstAudioGrace floors every computed budget for the 0.6b worker
	// (and as the scheduler/device floor for every native tier). Isolated
	// 0.6b short sentences synthesize in ~2.7s, so 8s is still plenty of
	// margin for a device open.
	minFirstAudioGrace = 8 * time.Second

	// minFirstAudioGrace17B is the 1.7b floor. The 8s value above assumed
	// a three-word reply synthesizes in well under a second. That is false
	// for native 1.7b in the product path: session 20260829-182727-10c0
	// spent 5.8s on "Hey!" (warm worker, Stage A, Metal, brain resident)
	// and then lost "What's on your mind?" (20 chars → exactly 8s of RTF
	// budget) to the Kokoro fallback. The RTF ceiling is fitted to longer
	// first segments, so it does not carry that per-request cost onto a
	// short closer.
	//
	// 16s is 2× the old floor: it covers the measured 5.8s short with
	// GPU-sharing margin, and a 1.7b worker restart (~11s) even when the
	// budget was armed while the session pointer still looked live.
	minFirstAudioGrace17B = 16 * time.Second
)

// qwenCommand starts one bounded native synthesis process. Keeping this as a
// seam makes the provider testable without downloading or vendoring models.
type qwenCommand func(context.Context, string, ...string) *exec.Cmd

// Qwen3TTS implements the optional Qwen3-TTS provider.
//
// Resolution order:
//  1. Explicit native worker binary (qwen3-tts-worker) + model dir
//  2. Installed native package under models_dir/qwen3-tts/ (product default)
//  3. External one-shot qwen3-tts-cli / worker (lab/debug only)
//
// There is no managed Python product path after cutover.
type Qwen3TTS struct {
	binary              string
	model               string
	modelTier           string // QWEN3_TTS_TIER for multi-tier native packages (0.6b / 1.7b)
	timeout             time.Duration
	timeoutSet          bool // true when cfg.QwenTTSTimeout was explicitly set
	command             qwenCommand
	alive               atomic.Bool
	mode                tts.VoiceMode
	voice               string
	language            string
	instruction         string
	referenceAudio      string
	referenceTranscript string
	modelsDir           string
	consent             bool
	managed             bool // always false after cutover (kept for test seams)
	native              bool // native qwen3-tts-worker protocol (product path)
	workerScript        string
	startupTimeout      time.Duration
	sessionMu           sync.Mutex
	session             *managedQwenSession // legacy tests only; product never sets managed
	speech              qwenSpeechSession
	speechRef           atomic.Pointer[qwenSpeechSession]
	// A live session is not warm evidence until it has completed a prior
	// request on that same session.
	speechCompleted atomic.Bool
	// startSpeech opens the transport this provider speaks through: the local
	// child process, or a speech-serve connection when tts_place is lan.
	startSpeech func(context.Context) (qwenSpeechSession, error)
	// remote reports that speech runs on another computer. It gates everything
	// that assumes a local worker: GGUF paths, ref_wav, disk presets.
	remote     bool
	remoteAddr string
	// streamMode is the resolved qwen_tts_stream setting and backendHint is
	// what the installed package says it was built against. Together with the
	// worker's advertised capability they decide whether this host plays PCM
	// chunks as they arrive; see config.QwenStreamEligible.
	streamMode  string
	backendHint string
}

// NewQwen3TTS resolves the native package or an explicit external CLI/worker.
// Product managed selection (empty binary/model) requires models_dir/qwen3-tts.
func NewQwen3TTS(cfg *config.Config) (*Qwen3TTS, error) {
	if cfg == nil {
		return nil, errors.New("qwen3-tts: nil config")
	}
	if err := config.ValidateQwenTTSConfig(cfg); err != nil {
		return nil, fmt.Errorf("qwen3-tts: %w", err)
	}
	if config.TTSPlaceOf(cfg) == config.TTSPlaceLAN {
		return newRemoteQwen3TTS(cfg)
	}

	binary := strings.TrimSpace(cfg.QwenTTSBinary)
	model := strings.TrimSpace(cfg.QwenTTSModel)
	modelTier := managedqwen.NormalizeModelTier(cfg.QwenTTSModelTier)
	modelsDir := config.ModelsDirFrom(cfg)
	managed := false
	native := false
	workerScript := ""
	backendHint := ""

	// Prefer explicit native worker binary.
	if binary != "" && isNativeWorkerBinary(binary) {
		if model == "" {
			return nil, errors.New("qwen3-tts: qwen_tts_model is required for the native worker")
		}
		native = true
	} else if install, ok := findNativeInstall(modelsDir, modelTier); ok && managedqwen.UseManaged(binary, model) {
		// Product default: empty binary/model → ensure-installed native package.
		binary = install.Worker
		model = install.ModelDir
		backendHint = install.BackendHint
		native = true
	} else if managedqwen.UseManaged(binary, model) {
		// No Python product path: require native package.
		return nil, errors.New("qwen3-tts: native package is not installed under models/qwen3-tts; set qwen_tts_native_url and run models ensure --tts (Settings → Qwen)")
	} else {
		if binary == "" {
			binary = "qwen3-tts-cli"
		}
		if model == "" {
			return nil, errors.New("qwen3-tts: qwen_tts_model is required for an external worker")
		}
		if isNativeWorkerBinary(binary) {
			native = true
		}
	}

	var binaryPath string
	var err error
	if filepath.IsAbs(binary) {
		binaryPath = binary
		if _, err := os.Stat(binaryPath); err != nil {
			return nil, fmt.Errorf("qwen3-tts: native worker %q not found: %w", binary, err)
		}
	} else {
		binaryPath, err = exec.LookPath(binary)
		if err != nil {
			return nil, fmt.Errorf("qwen3-tts: native worker %q not found: %w", binary, err)
		}
		if !filepath.IsAbs(binaryPath) {
			binaryPath, err = filepath.Abs(binaryPath)
			if err != nil {
				return nil, fmt.Errorf("qwen3-tts: resolve native worker %q: %w", binary, err)
			}
		}
	}
	if isNativeWorkerBinary(binaryPath) {
		native = true
		managed = false
	}
	modelInfo, err := os.Stat(model)
	if err != nil {
		return nil, fmt.Errorf("qwen3-tts: model directory %q: %w", model, err)
	}
	if !modelInfo.IsDir() {
		return nil, fmt.Errorf("qwen3-tts: model path %q is not a directory", model)
	}

	timeout := defaultQwenTTSTimeout
	timeoutSet := false
	if cfg.QwenTTSTimeout > 0 {
		timeout = time.Duration(cfg.QwenTTSTimeout) * time.Second
		timeoutSet = true
	}

	q := newQwen3TTS(binaryPath, model, timeout, exec.CommandContext)
	q.timeoutSet = timeoutSet
	q.modelTier = modelTier
	q.mode = tts.VoiceMode(strings.TrimSpace(cfg.QwenTTSMode))
	q.voice = strings.TrimSpace(cfg.QwenTTSVoice)
	q.language = strings.TrimSpace(cfg.QwenTTSLanguage)
	q.instruction = cfg.QwenTTSInstruction
	q.referenceAudio = strings.TrimSpace(cfg.QwenTTSReferenceAudio)
	q.referenceTranscript = cfg.QwenTTSReferenceText
	q.modelsDir = modelsDir
	q.consent = cfg.QwenTTSConsent
	q.managed = managed
	q.native = native
	q.workerScript = workerScript
	q.streamMode = config.QwenTTSStreamOf(cfg)
	q.backendHint = backendHint
	if managed || native {
		if q.mode == "" || q.mode == tts.VoiceModeStatic {
			q.mode = tts.VoiceModeCustomVoice
		}
		if q.voice == "" || strings.EqualFold(q.voice, "default") {
			q.voice = managedqwen.DefaultVoice
		} else if name, isShipped, ok := managedqwen.ResolveVoice(q.voice); ok {
			q.voice = name
			if isShipped {
				if err := bindShippedClone(context.Background(), &q.referenceAudio, &q.referenceTranscript, name, modelsDir); err != nil {
					return nil, fmt.Errorf("qwen3-tts: %w", err)
				}
			}
		}
		if q.language == "" {
			q.language = managedqwen.DefaultLanguage
		} else if language, ok := managedqwen.CanonicalLanguage(q.language); ok {
			q.language = language
		}
	}
	q.alive.Store(true)
	startupTimeout := max(timeout, 10*time.Minute)
	q.startupTimeout = startupTimeout
	if native {
		q.startSpeech = func(ctx context.Context) (qwenSpeechSession, error) {
			return startNativeQwenSession(ctx, binaryPath, model, q.modelTier, startupTimeout, q.backendHint)
		}
		session, err := q.startSpeech(context.Background())
		if err != nil {
			q.alive.Store(false)
			return nil, fmt.Errorf("qwen3-tts: %w", err)
		}
		q.applyStreamPolicy(session)
		q.speech = session
		q.speechRef.Store(&session)
	} else if managed {
		session, err := startManagedQwenSession(binaryPath, workerScript, model, startupTimeout)
		if err != nil {
			q.alive.Store(false)
			return nil, fmt.Errorf("qwen3-tts: %w", err)
		}
		q.session = session
	}
	return q, nil
}

func newQwen3TTS(binary, model string, timeout time.Duration, command qwenCommand) *Qwen3TTS {
	if timeout <= 0 {
		timeout = defaultQwenTTSTimeout
	}
	if command == nil {
		command = exec.CommandContext
	}
	return &Qwen3TTS{binary: binary, model: model, timeout: timeout, command: command}
}

// ProviderName identifies this provider in latency artifacts.
func (q *Qwen3TTS) ProviderName() string { return qwen3TTSProviderName }

// Available returns true while this provider may start native workers.
func (q *Qwen3TTS) Available() bool { return q.alive.Load() }

// Delete prevents new work. Existing workers are still bounded by their
// request context; callers should cancel that context during shutdown.
func (q *Qwen3TTS) Delete() {
	q.alive.Store(false)
	q.sessionMu.Lock()
	defer q.sessionMu.Unlock()
	q.discardManagedSessionLocked()
	q.discardSpeechSessionLocked()
}
