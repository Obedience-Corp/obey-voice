//go:build !integration

package meeting

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	meetinglog "github.com/Obedience-Corp/obey-voice/internal/meeting/log"
	"github.com/Obedience-Corp/obey-voice/internal/speaker"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
)

const speakerCaptureQueue = 1024

// workingAudioPartName is the transient streamed-PCM file used when the meeting
// is diarized but audio retention (record_audio) is off. It lives inside the
// bundle's hidden internal dir and is removed once diarization has read it:
// record_audio off means no audio survives the stop, resume included.
const workingAudioPartName = "audio.wav.part"

type captureSubscriber interface {
	Subscribe(int) (int, <-chan []float32)
	Unsubscribe(int)
}

// SpeakerSession owns the optional meeting PCM subscriber and post-capture
// analysis. It never runs model work on the capture callback or STT goroutine.
//
// Captured PCM is streamed straight to a WAV file on disk rather than buffered
// in memory for the whole meeting: a multi-hour recording keeps process memory
// flat, a crash leaves a valid partial WAV, and diarization re-reads the file at
// Finalize instead of retaining a second full copy.
type SpeakerSession struct {
	capture     captureSubscriber
	subID       int
	analyzer    SpeakerAnalyzer
	closer      interface{ Close() error }
	writer      *meetinglog.Writer
	bundlePath  string
	recordAudio bool

	// audioWriter streams capture chunks to workingPath. workingPath is the
	// final bundle audio.wav when record_audio is on, otherwise a transient
	// .part file that Finalize/Close remove after diarization.
	audioWriter *audio.WAVWriter
	workingPath string
	writeErr    error

	stop        sync.Once
	wg          sync.WaitGroup
	discardOnce sync.Once

	finalize sync.Once
	result   AnalysisResult
	err      error

	// prepare loads the native diarizer after capture has already started so
	// meeting record is not stuck on "Checking speaker models" / speaker ID.
	prepare   func(context.Context) (SpeakerAnalyzer, error)
	prepMu    sync.Mutex
	prepDone  chan struct{}
	prepErr   error
	abandoned bool
}

func NewSpeakerSession(capture captureSubscriber, analyzer SpeakerAnalyzer, writer *meetinglog.Writer, bundlePath string, recordAudio bool) (*SpeakerSession, error) {
	if capture == nil {
		return nil, fmt.Errorf("meeting: speaker capture is required")
	}
	if !strings.HasSuffix(strings.ToLower(filepath.Base(strings.TrimSpace(bundlePath))), ".meeting") {
		return nil, fmt.Errorf("meeting: .meeting bundle path is required for speaker analysis")
	}

	workingPath := speakerAudioPath(bundlePath)
	if !recordAudio {
		workingPath = filepath.Join(bundlePath, meetinglog.BundleInternalDirName, workingAudioPartName)
	}
	if err := os.MkdirAll(filepath.Dir(workingPath), 0o700); err != nil {
		return nil, fmt.Errorf("meeting: prepare speaker audio dir: %w", err)
	}
	audioWriter, err := openSpeakerAudio(workingPath)
	if err != nil {
		return nil, err
	}

	id, chunks := capture.Subscribe(speakerCaptureQueue)
	s := &SpeakerSession{
		capture: capture, subID: id, analyzer: analyzer, writer: writer,
		bundlePath: bundlePath, recordAudio: recordAudio,
		audioWriter: audioWriter, workingPath: workingPath,
	}
	if analyzer != nil {
		if closer, ok := analyzer.(interface{ Close() error }); ok {
			s.closer = closer
		}
	}
	s.wg.Go(func() {
		for chunk := range chunks {
			if s.writeErr != nil {
				continue // drain the subscription but stop writing after an error
			}
			if err := s.audioWriter.Write(chunk); err != nil {
				s.writeErr = err
			}
		}
	})
	return s, nil
}

// StartPrepare loads the diarizer in the background. Finalize waits on it.
// Capture is already running; this must not be called from the TUI startup
// path as a blocking load.
func (s *SpeakerSession) StartPrepare(ctx context.Context, prepare func(context.Context) (SpeakerAnalyzer, error)) {
	if s == nil || prepare == nil {
		return
	}
	s.prepMu.Lock()
	if s.analyzer != nil || s.prepDone != nil {
		s.prepMu.Unlock()
		return
	}
	s.prepare = prepare
	s.prepDone = make(chan struct{})
	s.prepMu.Unlock()
	go s.runPrepare(ctx)
}

func (s *SpeakerSession) runPrepare(ctx context.Context) {
	defer close(s.prepDone)
	a, err := s.prepare(ctx)
	s.prepMu.Lock()
	defer s.prepMu.Unlock()
	if err != nil {
		s.prepErr = err
		return
	}
	if s.abandoned {
		if closer, ok := a.(interface{ Close() error }); ok {
			_ = closer.Close()
		}
		return
	}
	s.analyzer = a
	if closer, ok := a.(interface{ Close() error }); ok {
		s.closer = closer
	}
}

func (s *SpeakerSession) waitAnalyzer(ctx context.Context) (SpeakerAnalyzer, error) {
	s.prepMu.Lock()
	a, err, done := s.analyzer, s.prepErr, s.prepDone
	s.prepMu.Unlock()
	if a != nil || err != nil {
		return a, err
	}
	if done == nil {
		return nil, fmt.Errorf("meeting: speaker analyzer is required")
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-done:
	}
	s.prepMu.Lock()
	defer s.prepMu.Unlock()
	return s.analyzer, s.prepErr
}

// openSpeakerAudio continues an existing recording's PCM when one is already
// there, so a resume extends a retained audio.wav rather than truncating it.
func openSpeakerAudio(workingPath string) (*audio.WAVWriter, error) {
	if _, err := os.Stat(workingPath); err == nil {
		w, err := audio.OpenWAVWriter(workingPath, audio.SampleRate)
		if err != nil {
			return nil, fmt.Errorf("meeting: append speaker audio file: %w", err)
		}
		return w, nil
	}
	w, err := audio.NewWAVWriter(workingPath, audio.SampleRate)
	if err != nil {
		return nil, fmt.Errorf("meeting: open speaker audio file: %w", err)
	}
	return w, nil
}

// StopCapture releases the PCM subscription without closing the analyzer, so
// mic/STT teardown can run at capture stop while background diarization still
// owns the native engine. Idempotent; Finalize and Close also call it.
func (s *SpeakerSession) StopCapture() { s.stopCapture() }

// stopCapture unsubscribes, joins the writer goroutine, and closes the streamed
// WAV so its size fields are finalized before any re-read. Idempotent.
func (s *SpeakerSession) stopCapture() {
	if s == nil {
		return
	}
	s.stop.Do(func() {
		s.capture.Unsubscribe(s.subID)
		s.wg.Wait()
		if s.audioWriter != nil {
			if err := s.audioWriter.Close(); err != nil && s.writeErr == nil {
				s.writeErr = err
			}
		}
	})
}

// Finalize stops PCM collection, runs offline diarization, and persists an
// additive analysis sidecar plus human/JSONL log records. It is idempotent.
func (s *SpeakerSession) Finalize(ctx context.Context) (AnalysisResult, error) {
	if s == nil {
		return AnalysisResult{Status: AnalysisDisabled}, nil
	}
	s.finalize.Do(func() {
		s.stopCapture()
		// waitAnalyzer is the only reader of s.analyzer that takes prepMu, and
		// the background load writes it under the same lock.
		analyzer, err := s.waitAnalyzer(ctx)
		if err != nil {
			s.result = AnalysisResult{Status: AnalysisError, Error: err.Error()}
			s.err = err
			return
		}
		artifact := speakerArtifactPath(s.bundlePath)
		var persistErr error
		if s.writeErr != nil {
			persistErr = errors.Join(persistErr, fmt.Errorf("record meeting audio: %w", s.writeErr))
		}

		// Re-read the streamed PCM for offline diarization instead of holding the
		// whole recording in memory for the meeting's duration.
		var samples []float32
		if s.audioWriter != nil && s.audioWriter.Samples() > 0 {
			got, _, rerr := audio.ReadWAVFloat32(s.workingPath)
			if rerr != nil {
				persistErr = errors.Join(persistErr, fmt.Errorf("read meeting audio: %w", rerr))
			} else {
				samples = got
			}
		}

		audioFile := ""
		if s.recordAudio {
			audioFile = s.workingPath // the streamed bundle audio.wav
			if err := os.Chmod(audioFile, 0o600); err != nil {
				persistErr = errors.Join(persistErr, fmt.Errorf("secure meeting audio: %w", err))
			}
		} else {
			// record_audio is off: keep the diarization result, not the audio.
			s.discardWorkingAudio()
		}

		// Where this recording segment sits in the meeting: 0 on a fresh
		// bundle, the resume baseline on a reopened one.
		segmentStartMS := int64(0)
		if s.writer != nil {
			segmentStartMS = s.writer.SegmentStartMS()
		}
		// What the audio just read actually covers. With retention on it is the
		// whole meeting; with it off the working PCM was discarded at the
		// previous stop, so a resumed pass holds only its own segment and its
		// timeline starts at that segment, not at the meeting.
		windowStartMS := int64(0)
		if !s.recordAudio {
			windowStartMS = segmentStartMS
		}

		result := AnalyzeRecording(ctx, analyzer, samples)
		result.Timeline = shiftTimeline(result.Timeline, windowStartMS)
		result.Artifact = artifact
		result.AudioFile = audioFile
		result.SpeakerCount = distinctSpeakerCount(result.Timeline)
		if persistErr != nil {
			result.Error = joinErrorText(result.Error, persistErr)
			if result.Status == AnalysisComplete {
				result.Status = AnalysisError
			}
		}
		if err := WriteAnalysis(artifact, result); err != nil {
			persistErr = errors.Join(persistErr, err)
			result.Error = joinErrorText(result.Error, err)
			if result.Status == AnalysisComplete {
				result.Status = AnalysisError
			}
		}

		if s.writer != nil {
			// Only the utterances inside the analyzed window are re-attributed.
			// Lines from before it keep the labels their own pass gave them:
			// the earlier speaker_utterance events are left standing, and
			// readers collapse by id, so nothing is relabelled from audio that
			// no longer exists.
			analysis := logAnalysis(result,
				transcriptsFrom(s.writer.Transcripts(), windowStartMS),
				segmentIDPrefix(segmentStartMS))
			if err := s.writer.WriteSpeakerAnalysis(analysis); err != nil {
				persistErr = errors.Join(persistErr, err)
				result.Error = joinErrorText(result.Error, err)
				if result.Status == AnalysisComplete {
					result.Status = AnalysisError
				}
			}
		}
		s.result, s.err = result, persistErr
		s.prepMu.Lock()
		closer := s.closer
		s.prepMu.Unlock()
		if closer != nil {
			s.err = errors.Join(s.err, closer.Close())
		}
	})
	return s.result, s.err
}

// discardWorkingAudio removes the streamed PCM when audio retention is off.
// Safe to call from both Finalize and Close.
func (s *SpeakerSession) discardWorkingAudio() {
	if s == nil || s.recordAudio || s.workingPath == "" {
		return
	}
	s.discardOnce.Do(func() { _ = os.Remove(s.workingPath) })
}

// shiftTimeline moves observations from analyzed-audio time into meeting time.
func shiftTimeline(timeline speaker.Timeline, offsetMS int64) speaker.Timeline {
	if offsetMS <= 0 {
		return timeline
	}
	for i := range timeline.Observations {
		timeline.Observations[i].StartMS += offsetMS
		timeline.Observations[i].EndMS += offsetMS
	}
	return timeline
}

// transcriptsFrom keeps the records the analyzed audio covers. A line that
// began before the window belongs to an earlier pass.
func transcriptsFrom(records []meetinglog.TranscriptRecord, startMS int64) []meetinglog.TranscriptRecord {
	if startMS <= 0 {
		return records
	}
	kept := make([]meetinglog.TranscriptRecord, 0, len(records))
	for _, record := range records {
		if record.StartMS >= startMS {
			kept = append(kept, record)
		}
	}
	return kept
}

func joinErrorText(existing string, err error) string {
	if err == nil {
		return existing
	}
	if existing == "" {
		return err.Error()
	}
	return errors.Join(errors.New(existing), err).Error()
}

// segmentIDPrefix namespaces one pass's diarization segment ids. Every pass's
// diarizer numbers its own segments from 1, so a resumed bundle would hold two
// diarization-1 events otherwise. The first pass keeps its bare ids, leaving
// existing bundles untouched.
func segmentIDPrefix(segmentStartMS int64) string {
	if segmentStartMS <= 0 {
		return ""
	}
	return fmt.Sprintf("r%d-", segmentStartMS)
}

func logAnalysis(result AnalysisResult, transcripts []meetinglog.TranscriptRecord, segmentIDPrefix string) meetinglog.SpeakerAnalysis {
	analysis := meetinglog.SpeakerAnalysis{
		Status: string(result.Status), Error: result.Error,
		Artifact: result.Artifact, AudioFile: result.AudioFile,
	}
	for _, obs := range result.Timeline.Observations {
		id := obs.SegmentID
		if id != "" {
			id = segmentIDPrefix + id
		}
		analysis.Segments = append(analysis.Segments, meetinglog.SpeakerSegment{
			ID: id, StartMS: obs.StartMS, EndMS: obs.EndMS,
			Label: obs.Label, Confidence: obs.Confidence, State: string(obs.State),
		})
	}
	segments := make([]TranscriptSegment, 0, len(transcripts))
	for _, transcript := range transcripts {
		segments = append(segments, TranscriptSegment{
			ID: transcript.ID, StartMS: transcript.StartMS,
			EndMS: transcript.EndMS, Text: transcript.Text,
		})
	}
	for _, attributed := range AttributeTranscript(segments, result.Timeline) {
		analysis.Utterances = append(analysis.Utterances, meetinglog.SpeakerUtterance{
			TranscriptRecord: meetinglog.TranscriptRecord{
				ID: attributed.ID, StartMS: attributed.StartMS,
				EndMS: attributed.EndMS, Text: attributed.Text,
			},
			Speaker: attributed.Speaker, Confidence: attributed.Confidence, State: string(attributed.State),
		})
	}
	return analysis
}

func distinctSpeakerCount(timeline speaker.Timeline) int {
	labels := make(map[string]struct{})
	for _, observation := range timeline.Observations {
		if observation.Label != "" && observation.Label != speaker.LabelUnknown {
			labels[observation.Label] = struct{}{}
		}
	}
	return len(labels)
}

func speakerArtifactPath(bundlePath string) string {
	return filepath.Join(bundlePath, meetinglog.BundleInternalDirName, meetinglog.BundleSpeakerAnalysisName)
}

func speakerAudioPath(bundlePath string) string {
	return filepath.Join(bundlePath, meetinglog.BundleAudioName)
}

// Close stops collection and releases native resources when a meeting exits
// before normal finalization.
func (s *SpeakerSession) Close() error {
	if s == nil {
		return nil
	}
	s.stopCapture()
	// If the meeting aborted before Finalize and audio was not opted in, do not
	// leave the streamed PCM behind.
	s.discardWorkingAudio()
	s.prepMu.Lock()
	s.abandoned = true
	closer := s.closer
	s.prepMu.Unlock()
	if closer != nil {
		return closer.Close()
	}
	return nil
}
