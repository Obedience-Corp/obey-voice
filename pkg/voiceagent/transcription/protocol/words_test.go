package protocol

import (
	"bytes"
	"io"
	"strings"
	"testing"
)

func wordStream(t *testing.T, mutate func(*Segment)) []byte {
	t.Helper()
	var buf bytes.Buffer
	enc := NewWordEncoder(&buf, "word-job")
	a := NewAccepted("parakeet", "tdt-0.6b-v2", "sha", TimingWord)
	a.TextPolicy = "verbatim"
	a.TimingMethod = "parakeet-tdt-token-duration"
	a.TimingValidity = "estimated"
	a.Language = "en"
	a.Runtime = "sherpa-onnx/1.13.8"
	s := NewSegment("s1", 100000, 600000, "Um, yes.", TimingWord)
	s.Words = []Word{{"w1", "Um,", 100000, 300000}, {"w2", "yes.", 350000, 600000}}
	if mutate != nil {
		mutate(s)
	}
	for _, ev := range []Event{a, s, NewCompleted(1000000, 1, OutcomeSpeech)} {
		if err := enc.Encode(ev); err != nil {
			t.Fatal(err)
		}
	}
	return buf.Bytes()
}
func TestWordStreamRequiresExplicitVersionAndValidBoundaries(t *testing.T) {
	good := wordStream(t, nil)
	d := NewWordDecoder(bytes.NewReader(good), "word-job")
	for {
		_, err := d.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	if !d.Completed() {
		t.Fatal("not completed")
	}
	if _, err := NewDecoder(bytes.NewReader(good), "word-job").Next(); err == nil {
		t.Fatal("v1 reader accepted v2")
	}
	for _, tc := range []struct {
		name   string
		mutate func(*Segment)
	}{
		{"overlap", func(s *Segment) { s.Words[1].StartUS = 200000 }},
		{"missing end", func(s *Segment) { s.Words[0].EndUS = 0 }},
		{"out of bounds", func(s *Segment) { s.Words[1].EndUS = 700000 }},
		{"duplicate", func(s *Segment) { s.Words[1].ID = "w1" }},
		{"rewritten text", func(s *Segment) { s.Text = "yes." }},
		{"utterance only", func(s *Segment) { s.Words = nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := NewWordDecoder(bytes.NewReader(wordStream(t, tc.mutate)), "word-job")
			if _, err := d.Next(); err != nil {
				t.Fatal(err)
			}
			if _, err := d.Next(); err == nil {
				t.Fatal("accepted bad words")
			}
		})
	}
}
func TestWordRequestRequiresVerbatim(t *testing.T) {
	req, err := ParseRequest(bytes.NewBufferString(`{"protocol":"obey-transcript/v2","job_id":"w","input":{"path":"/a.wav","format":"wav_pcm_s16le","sample_rate_hz":16000,"channels":1,"timeline_origin_us":0},"timing":"word","text_policy":"verbatim"}`))
	if err != nil {
		t.Fatal(err)
	}
	if err = req.Validate(); err != nil {
		t.Fatal(err)
	}
	req.TextPolicy = "corrected"
	if req.Validate() == nil {
		t.Fatal("accepted corrected word request")
	}
}

func TestWordStreamRequiresPresentZeroBoundary(t *testing.T) {
	good := wordStream(t, nil)
	for _, bad := range []string{
		strings.Replace(string(good), `"start_us":100000,"end_us":300000`, `"end_us":300000`, 1),
		strings.Replace(string(good), `"start_us":100000,"end_us":300000`, `"start_us":null,"end_us":300000`, 1),
	} {
		d := NewWordDecoder(strings.NewReader(bad), "word-job")
		if _, err := d.Next(); err != nil {
			t.Fatal(err)
		}
		if _, err := d.Next(); err == nil {
			t.Fatal("accepted missing measured start")
		}
	}
}
