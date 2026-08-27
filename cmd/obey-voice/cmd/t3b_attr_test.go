//go:build !integration

package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"testing"

	"github.com/Obedience-Corp/obey-voice/internal/meeting"
	meetinglog "github.com/Obedience-Corp/obey-voice/internal/meeting/log"
	"github.com/Obedience-Corp/obey-voice/internal/speaker"
)

type transcriptDump struct {
	Fixture     string                        `json:"fixture"`
	Provider    string                        `json:"stt_provider"`
	Transcripts []meetinglog.TranscriptRecord `json:"transcripts"`
}

type analysisDump struct {
	Timeline speaker.Timeline `json:"timeline"`
}

type sortformerSpan struct {
	T0        int64 `json:"t0_ms"`
	T1        int64 `json:"t1_ms"`
	SpeakerID int   `json:"speaker_id"`
}

// T3b step 2: run the REAL meeting.AttributeTranscript over the real
// transcript records with (a) the sherpa timeline from `meeting analyze`
// (control) and (b) the Sortformer timeline from transcribe-cli.
func TestT3bAttribute(t *testing.T) {
	tPath := os.Getenv("T3B_TRANSCRIPTS")
	sherpaPath := os.Getenv("T3B_SHERPA")
	sortPath := os.Getenv("T3B_SORTFORMER")
	out := os.Getenv("T3B_ATTR_OUT")
	if tPath == "" || sherpaPath == "" || sortPath == "" || out == "" {
		t.Skip("set T3B_TRANSCRIPTS, T3B_SHERPA, T3B_SORTFORMER, T3B_ATTR_OUT")
	}

	var td transcriptDump
	readJSON(t, tPath, &td)
	segments := make([]meeting.TranscriptSegment, 0, len(td.Transcripts))
	for _, r := range td.Transcripts {
		segments = append(segments, meeting.TranscriptSegment{
			ID: r.ID, StartMS: r.StartMS, EndMS: r.EndMS, Text: r.Text,
		})
	}

	var ad analysisDump
	readJSON(t, sherpaPath, &ad)
	sherpa := ad.Timeline

	sortformer := speaker.Timeline{}
	spans := readSortformer(t, sortPath)
	for i, s := range spans {
		sortformer.Observations = append(sortformer.Observations, speaker.Observation{
			SegmentID:  fmt.Sprintf("sortformer-%d", i+1),
			StartMS:    s.T0,
			EndMS:      s.T1,
			Label:      fmt.Sprintf("%s%d", speaker.LabelSpeakerPrefix, s.SpeakerID),
			Confidence: 0,
			State:      speaker.StateStable,
			Source:     speaker.SourceRecording,
			ModelRev:   "diar_streaming_sortformer_4spk-v2.1-Q8_0",
		})
	}

	result := struct {
		Segments   []meeting.TranscriptSegment `json:"segments"`
		Sherpa     []meeting.AttributedSegment `json:"sherpa_attributed"`
		Sortformer []meeting.AttributedSegment `json:"sortformer_attributed"`
		Timelines  map[string]speaker.Timeline `json:"timelines"`
	}{
		Segments:   segments,
		Sherpa:     meeting.AttributeTranscript(segments, sherpa),
		Sortformer: meeting.AttributeTranscript(segments, sortformer),
		Timelines:  map[string]speaker.Timeline{"sherpa": sherpa, "sortformer": sortformer},
	}
	blob, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(out, append(blob, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Logf("segments=%d sherpa_obs=%d sortformer_obs=%d -> %s",
		len(segments), len(sherpa.Observations), len(sortformer.Observations), out)
}

func readJSON(t *testing.T, path string, v any) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, v); err != nil {
		t.Fatal(err)
	}
}

func readSortformer(t *testing.T, path string) []sortformerSpan {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var out []sortformerSpan
	for _, line := range splitLines(string(data)) {
		if line == "" {
			continue
		}
		var rec struct {
			Speakers []sortformerSpan `json:"speakers"`
		}
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			continue
		}
		if len(rec.Speakers) > 0 {
			out = append(out, rec.Speakers...)
		}
	}
	if len(out) == 0 {
		t.Fatalf("no sortformer spans in %s", path)
	}
	return out
}

func splitLines(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	out = append(out, s[start:])
	return out
}
