//go:build !integration

package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"testing"

	"github.com/Obedience-Corp/obey-voice/internal/meeting"
	"github.com/Obedience-Corp/obey-voice/internal/speaker"
)

// T3b step 3: timing-robustness sweep. Meeting transcript timestamps are
// estimated (log.Writer.OnUtterance: end = utterance wall clock, start = end -
// 350 ms/word), so the interesting question is how much timestamp error the
// attribution rule tolerates before Sortformer's gaps start dropping turns.
// Everything below calls the shipped meeting.AttributeTranscript.
func TestT3bShiftSweep(t *testing.T) {
	tPath := os.Getenv("T3B_TRANSCRIPTS")
	sherpaPath := os.Getenv("T3B_SHERPA")
	sortPath := os.Getenv("T3B_SORTFORMER")
	out := os.Getenv("T3B_SHIFT_OUT")
	if tPath == "" || sherpaPath == "" || sortPath == "" || out == "" {
		t.Skip("set T3B_TRANSCRIPTS, T3B_SHERPA, T3B_SORTFORMER, T3B_SHIFT_OUT")
	}
	var td transcriptDump
	readJSON(t, tPath, &td)
	base := make([]meeting.TranscriptSegment, 0, len(td.Transcripts))
	for _, r := range td.Transcripts {
		base = append(base, meeting.TranscriptSegment{ID: r.ID, StartMS: r.StartMS, EndMS: r.EndMS, Text: r.Text})
	}
	var ad analysisDump
	readJSON(t, sherpaPath, &ad)
	sherpa := ad.Timeline
	sortformer := speaker.Timeline{}
	for i, s := range readSortformer(t, sortPath) {
		sortformer.Observations = append(sortformer.Observations, speaker.Observation{
			SegmentID: fmt.Sprintf("sortformer-%d", i+1), StartMS: s.T0, EndMS: s.T1,
			Label: fmt.Sprintf("%s%d", speaker.LabelSpeakerPrefix, s.SpeakerID),
			State: speaker.StateStable, Source: speaker.SourceRecording,
		})
	}

	type disagreement struct {
		ID         string `json:"id"`
		Sherpa     string `json:"sherpa"`
		Sortformer string `json:"sortformer"`
	}
	type row struct {
		ShiftMS            int64          `json:"shift_ms"`
		SherpaUnlabelled   int            `json:"sherpa_unlabelled"`
		SherpaUnlabelledMS int64          `json:"sherpa_unlabelled_ms"`
		SortUnlabelled     int            `json:"sortformer_unlabelled"`
		SortUnlabelledMS   int64          `json:"sortformer_unlabelled_ms"`
		Disagree           int            `json:"label_disagreements"`
		DisagreeMS         int64          `json:"label_disagreements_ms"`
		SortUnlabelledIDs  []string       `json:"sortformer_unlabelled_ids,omitempty"`
		Disagreements      []disagreement `json:"disagreements,omitempty"`
	}
	var rows []row
	for shift := int64(-2000); shift <= 2000; shift += 100 {
		segs := make([]meeting.TranscriptSegment, len(base))
		for i, s := range base {
			s.StartMS += shift
			s.EndMS += shift
			if s.StartMS < 0 {
				s.StartMS = 0
			}
			segs[i] = s
		}
		a := meeting.AttributeTranscript(segs, sherpa)
		b := meeting.AttributeTranscript(segs, sortformer)
		r := row{ShiftMS: shift}
		for i := range segs {
			d := segs[i].EndMS - segs[i].StartMS
			if a[i].Speaker == speaker.LabelUnknown {
				r.SherpaUnlabelled++
				r.SherpaUnlabelledMS += d
			}
			if b[i].Speaker == speaker.LabelUnknown {
				r.SortUnlabelled++
				r.SortUnlabelledMS += d
				r.SortUnlabelledIDs = append(r.SortUnlabelledIDs, segs[i].ID)
			}
			if a[i].Speaker != b[i].Speaker {
				r.Disagree++
				r.DisagreeMS += d
				r.Disagreements = append(r.Disagreements, disagreement{
					ID: segs[i].ID, Sherpa: a[i].Speaker, Sortformer: b[i].Speaker,
				})
			}
		}
		rows = append(rows, r)
	}
	blob, _ := json.MarshalIndent(rows, "", "  ")
	if err := os.WriteFile(out, append(blob, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		t.Logf("shift=%+5d sherpa_unlab=%d(%dms) sort_unlab=%d(%dms) disagree=%d(%dms) %v",
			r.ShiftMS, r.SherpaUnlabelled, r.SherpaUnlabelledMS, r.SortUnlabelled, r.SortUnlabelledMS,
			r.Disagree, r.DisagreeMS, r.SortUnlabelledIDs)
	}
}
