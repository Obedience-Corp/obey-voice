package transcription

import "testing"

func TestWordWindowsCoverEverySampleIncludingQuietEdges(t *testing.T) {
	d := NewWindowSpanDetector(16000)
	var spans []Span
	const total = 16000*63 + 217
	for offset := 0; offset < total; {
		n := min(4096, total-offset)
		pcm := make([]float32, n)
		for i := range pcm {
			pcm[i] = float32(offset+i) * 1e-12
		}
		d.Accept(pcm)
		offset += n
		for {
			s, ok := d.Next()
			if !ok {
				break
			}
			spans = append(spans, s)
		}
	}
	d.Flush()
	for {
		s, ok := d.Next()
		if !ok {
			break
		}
		spans = append(spans, s)
	}
	var end int64
	for _, s := range spans {
		if s.StartSample != end || len(s.Samples) > 320000 || s.EndSample-s.StartSample != int64(len(s.Samples)) {
			t.Fatalf("bad window %+v", s)
		}
		for i, v := range s.Samples {
			if v != float32(s.StartSample+int64(i))*1e-12 {
				t.Fatal("sample omitted or duplicated")
			}
		}
		end = s.EndSample
	}
	if end != total || d.Fed() != total {
		t.Fatalf("got %d of %d", end, total)
	}
}
