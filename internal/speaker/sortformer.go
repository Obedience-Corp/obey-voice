package speaker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

// Sortformer runs through the transcribe.cpp CLI in batch mode, the same
// subprocess seam the optional transcribecpp STT provider uses. That is not
// only convenience: upstream 0.2.2 allows one in-flight compute per loaded
// model (design D8), and a separate process is a separate load, so meeting
// review can run while a transcribe.cpp STT turn is in flight.
const (
	// sortformerPresetEnv selects the streaming operating point. transcribe-cli
	// has no flag for it; the env var is the only selector upstream reads.
	sortformerPresetEnv = "TRANSCRIBE_SORTFORMER_STREAM_PRESET"
	// sortformerPreset is the offline operating point T3/T3b measured. The
	// lower-latency presets emit more spans for no accuracy gain on a recording
	// that is already complete.
	sortformerPreset = "very_high_latency"
	// sortformerBackend lets the CLI pick its fastest lane. Unlike the STT
	// provider there is no CPU refusal: Sortformer on CPU is still 2.6x the
	// sherpa review path it replaces (T3).
	sortformerBackend = "auto"
)

// SortformerOptions is a resolved transcribe.cpp diarizer invocation.
type SortformerOptions struct {
	// Binary is the transcribe-cli name or path (transcribecpp_binary).
	Binary string
	// ModelPath is the Sortformer GGUF on disk.
	ModelPath string
	// ModelRev stamps Observation.ModelRev; empty uses the shipped revision.
	ModelRev string
}

// SortformerDiarizer implements the offline review pass with NVIDIA Streaming
// Sortformer. It produces speaker spans only — no transcription — and has no
// speaker-count input, so Diarize ignores numSpeakers rather than faking one.
// It holds no native resources: every run is its own process.
type SortformerDiarizer struct {
	binary    string
	modelPath string
	modelRev  string
}

// SortformerOptionsFromConfig resolves the CLI and the managed GGUF the
// diarizer needs from app config.
func SortformerOptionsFromConfig(cfg *config.Config, modelsDir string) SortformerOptions {
	binary := ""
	if cfg != nil {
		binary = cfg.TranscribeCPPBinary
	}
	return SortformerOptions{
		Binary:    binary,
		ModelPath: config.SortformerDiarizerAsset().ModelPath(modelsDir),
		ModelRev:  config.SortformerModelRevision,
	}
}

// NewSortformerDiarizer resolves the CLI and the weights up front so a
// misconfigured install fails when the meeting starts rather than at the end of
// one, with the recording already on disk and nothing left to retry with.
func NewSortformerDiarizer(ctx context.Context, opts SortformerOptions) (*SortformerDiarizer, error) {
	configured := strings.TrimSpace(opts.Binary)
	if configured == "" {
		return nil, fmt.Errorf("speaker: transcribecpp_binary is not set; the sortformer diarizer runs through the transcribe.cpp CLI")
	}
	binaryPath, err := exec.LookPath(configured)
	if err != nil {
		return nil, fmt.Errorf("speaker: transcribe.cpp binary %q not found in PATH; install the transcribe.cpp release CLI or set transcribecpp_binary to its path", configured)
	}
	modelPath := strings.TrimSpace(opts.ModelPath)
	if _, err := os.Stat(modelPath); err != nil {
		return nil, fmt.Errorf("speaker: sortformer model not found at %s; run 'obey-voice models ensure' to download it", modelPath)
	}
	// Probing the lanes proves the pinned CLI actually runs here. The result is
	// informational (any lane works), so it is not stored.
	if _, err := config.TranscribeCPPLanes(ctx, binaryPath); err != nil {
		return nil, fmt.Errorf("speaker: sortformer diarizer: %w", err)
	}

	modelRev := strings.TrimSpace(opts.ModelRev)
	if modelRev == "" {
		modelRev = config.SortformerModelRevision
	}
	return &SortformerDiarizer{binary: binaryPath, modelPath: modelPath, modelRev: modelRev}, nil
}

// Diarize writes the recording to a temp 16 kHz wav — the only rate the CLI
// accepts — and reads speaker spans back from its batch JSONL. numSpeakers is
// ignored: Sortformer resolves up to config.SortformerMaxSpeakers on its own
// and takes no count. The caller's context is what cancels the run, so a Ctrl+C
// during background review kills the subprocess instead of waiting it out.
func (d *SortformerDiarizer) Diarize(ctx context.Context, samples []float32, _ int) (Timeline, error) {
	if err := ctx.Err(); err != nil {
		return Timeline{}, err
	}
	if len(samples) == 0 {
		return Timeline{}, nil
	}

	tempDir, err := os.MkdirTemp("", "obey-voice-sortformer-*")
	if err != nil {
		return Timeline{}, fmt.Errorf("speaker: sortformer temp dir: %w", err)
	}
	defer os.RemoveAll(tempDir)

	audioPath := filepath.Join(tempDir, "meeting.wav")
	if err := audio.WriteWAVFloat32(audioPath, audio.SampleRate, samples); err != nil {
		return Timeline{}, fmt.Errorf("speaker: sortformer temp wav: %w", err)
	}
	batchPath := filepath.Join(tempDir, "files.txt")
	if err := os.WriteFile(batchPath, []byte(audioPath+"\n"), 0o600); err != nil {
		return Timeline{}, fmt.Errorf("speaker: sortformer batch list: %w", err)
	}

	cmd := exec.CommandContext(ctx, d.binary,
		"-m", d.modelPath,
		"--backend", sortformerBackend,
		"-q",
		"--batch", batchPath,
		"--batch-jsonl",
	)
	cmd.Env = append(os.Environ(), sortformerPresetEnv+"="+sortformerPreset)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.Output()
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return Timeline{}, ctxErr
		}
		return Timeline{}, fmt.Errorf("speaker: sortformer: %w (%s)", err, strings.TrimSpace(stderr.String()))
	}

	return parseSortformerJSONL(stdout, d.modelRev)
}

// sortformerSpan is one entry of a batch JSONL record's speakers array.
type sortformerSpan struct {
	StartMS   int64 `json:"t0_ms"`
	EndMS     int64 `json:"t1_ms"`
	SpeakerID int   `json:"speaker_id"`
}

// parseSortformerJSONL maps the CLI's batch output onto observations, using the
// mapping the T3b probe measured through AttributeTranscript: stable state,
// recording source, speaker-<id> labels (the CLI's ids are 1-based, unlike the
// sherpa engine's) and the model revision on every span.
//
// The CLI groups its spans by speaker rather than by time, so the timeline is
// sorted before ids are assigned: renderers read observations in order, and a
// review that jumps backwards mid-meeting reads as a bug. Sorting moves no
// boundary and renames no speaker, so the gated attribution is unchanged.
func parseSortformerJSONL(out []byte, modelRev string) (Timeline, error) {
	var spans []sortformerSpan
	decoder := json.NewDecoder(bytes.NewReader(out))
	for {
		var record struct {
			Speakers []sortformerSpan `json:"speakers"`
		}
		if err := decoder.Decode(&record); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return Timeline{}, fmt.Errorf("speaker: sortformer output: %w", err)
		}
		spans = append(spans, record.Speakers...)
	}

	sort.SliceStable(spans, func(i, j int) bool {
		if spans[i].StartMS != spans[j].StartMS {
			return spans[i].StartMS < spans[j].StartMS
		}
		return spans[i].EndMS < spans[j].EndMS
	})

	observations := make([]Observation, 0, len(spans))
	for _, span := range spans {
		// Skip what the contract cannot express rather than inventing a label:
		// ids are 1-based and spans are non-empty in every measured run.
		if span.EndMS <= span.StartMS || span.SpeakerID < 1 {
			continue
		}
		observations = append(observations, Observation{
			SegmentID: fmt.Sprintf("sortformer-%d", len(observations)+1),
			StartMS:   span.StartMS,
			EndMS:     span.EndMS,
			Label:     AnonymousLabel(span.SpeakerID),
			State:     StateStable,
			Source:    SourceRecording,
			ModelRev:  modelRev,
		})
	}
	return Timeline{Observations: observations}, nil
}
