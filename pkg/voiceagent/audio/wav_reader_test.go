package audio

import (
	"bytes"
	"encoding/binary"
	"strings"
	"testing"
)

type wavSpec struct {
	tag, channels, bits          uint16
	rate                         uint32
	extra                        []wavChunk // chunks between fmt and data
	dataBytes                    uint32     // declared data length
	dataPresent                  int        // bytes of data actually written
	fmtLen                       int        // 0 means 16
	skipFmt, skipData, dataFirst bool
}

type wavChunk struct {
	id      string
	payload []byte
}

func buildWAV(s wavSpec) []byte {
	var body bytes.Buffer
	writeChunk := func(id string, payload []byte, declared uint32) {
		body.WriteString(id)
		_ = binary.Write(&body, binary.LittleEndian, declared)
		body.Write(payload)
		if len(payload)%2 == 1 {
			body.WriteByte(0)
		}
	}
	fmtPayload := make([]byte, 16)
	binary.LittleEndian.PutUint16(fmtPayload[0:], s.tag)
	binary.LittleEndian.PutUint16(fmtPayload[2:], s.channels)
	binary.LittleEndian.PutUint32(fmtPayload[4:], s.rate)
	binary.LittleEndian.PutUint16(fmtPayload[14:], s.bits)
	if s.fmtLen != 0 {
		fmtPayload = fmtPayload[:s.fmtLen]
	}
	data := make([]byte, s.dataPresent)
	if s.dataFirst && !s.skipData {
		writeChunk("data", data, s.dataBytes)
	}
	if !s.skipFmt {
		writeChunk("fmt ", fmtPayload, uint32(len(fmtPayload)))
	}
	for _, c := range s.extra {
		writeChunk(c.id, c.payload, uint32(len(c.payload)))
	}
	if !s.dataFirst && !s.skipData {
		writeChunk("data", data, s.dataBytes)
	}

	var out bytes.Buffer
	out.WriteString("RIFF")
	_ = binary.Write(&out, binary.LittleEndian, uint32(4+body.Len()))
	out.WriteString("WAVE")
	out.Write(body.Bytes())
	return out.Bytes()
}

func TestReadWAVFormatAccepts(t *testing.T) {
	mono16k := wavSpec{tag: 1, channels: 1, bits: 16, rate: 16000, dataBytes: 3200, dataPresent: 3200}

	withList := mono16k
	withList.extra = []wavChunk{{id: "LIST", payload: make([]byte, 34)}}

	withOddChunk := mono16k
	withOddChunk.extra = []wavChunk{{id: "junk", payload: make([]byte, 7)}}

	stereo := wavSpec{tag: 1, channels: 2, bits: 16, rate: 48000, dataBytes: 4000, dataPresent: 4000}
	empty := wavSpec{tag: 1, channels: 1, bits: 16, rate: 16000}

	cases := []struct {
		name       string
		spec       wavSpec
		wantOffset int64
		wantFrames int64
	}{
		{"canonical 44 byte header", mono16k, 44, 1600},
		{"LIST chunk before data", withList, 44 + 8 + 34, 1600},
		{"odd sized chunk is padded", withOddChunk, 44 + 8 + 7 + 1, 1600},
		{"stereo 48k is described, not rejected", stereo, 44, 1000},
		{"empty data chunk", empty, 44, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw := buildWAV(tc.spec)
			got, err := ReadWAVFormat(bytes.NewReader(raw), int64(len(raw)))
			if err != nil {
				t.Fatalf("ReadWAVFormat: %v", err)
			}
			if got.DataOffset != tc.wantOffset {
				t.Errorf("DataOffset = %d, want %d", got.DataOffset, tc.wantOffset)
			}
			if got.Frames() != tc.wantFrames {
				t.Errorf("Frames = %d, want %d", got.Frames(), tc.wantFrames)
			}
		})
	}
}

func TestReadWAVFormatRejects(t *testing.T) {
	base := wavSpec{tag: 1, channels: 1, bits: 16, rate: 16000, dataBytes: 3200, dataPresent: 3200}
	mut := func(f func(*wavSpec)) wavSpec { s := base; f(&s); return s }

	cases := []struct {
		name string
		raw  []byte
		want string // substring of the error
	}{
		{"not riff", []byte("OggS00000000"), "not a RIFF/WAVE"},
		{"short header", []byte("RIFF"), "read wav header"},
		{"float format", buildWAV(mut(func(s *wavSpec) { s.tag = 3 })), "format tag 3"},
		{"8 bit", buildWAV(mut(func(s *wavSpec) { s.bits = 8 })), "bit depth 8"},
		{"24 bit", buildWAV(mut(func(s *wavSpec) { s.bits = 24 })), "bit depth 24"},
		{"zero channels", buildWAV(mut(func(s *wavSpec) { s.channels = 0 })), "0 channels"},
		{"zero rate", buildWAV(mut(func(s *wavSpec) { s.rate = 0 })), "sample rate of 0"},
		{"short fmt chunk", buildWAV(mut(func(s *wavSpec) { s.fmtLen = 14 })), "fmt chunk is 14 bytes"},
		{"missing fmt", buildWAV(mut(func(s *wavSpec) { s.skipFmt = true })), "before fmt"},
		{"missing data", buildWAV(mut(func(s *wavSpec) { s.skipData = true })), "missing its fmt or data"},
		{"data before fmt", buildWAV(mut(func(s *wavSpec) { s.dataFirst = true })), "before fmt"},
		{"truncated data", buildWAV(mut(func(s *wavSpec) { s.dataPresent = 100 })), "only 100 remain"},
		{"partial frame", buildWAV(mut(func(s *wavSpec) { s.channels = 2; s.dataBytes = 3202; s.dataPresent = 3202 })), "whole number"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ReadWAVFormat(bytes.NewReader(tc.raw), int64(len(tc.raw)))
			if err == nil {
				t.Fatal("expected an error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not mention %q", err, tc.want)
			}
		})
	}
}

type countingReader struct {
	*bytes.Reader
	read int
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.Reader.Read(p)
	c.read += n
	return n, err
}

func TestReadWAVFormatDoesNotReadSamples(t *testing.T) {
	raw := buildWAV(wavSpec{tag: 1, channels: 1, bits: 16, rate: 16000, dataBytes: 1 << 20, dataPresent: 1 << 20})
	r := &countingReader{Reader: bytes.NewReader(raw)}
	if _, err := ReadWAVFormat(r, int64(len(raw))); err != nil {
		t.Fatal(err)
	}
	if r.read > 64 {
		t.Fatalf("read %d bytes of a 1 MiB file; the header is 44", r.read)
	}
}
