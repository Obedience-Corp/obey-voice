package audio

import (
	"encoding/binary"
	"testing"
)

func TestApplyVolumeS16UnityIsIdentity(t *testing.T) {
	buf := pcmS16([]int16{1234, -1234, 32767, -32768})
	orig := append([]byte(nil), buf...)
	applyVolumeS16(buf, 4, 100)
	if string(buf) != string(orig) {
		t.Fatal("unity gain mutated samples")
	}
}

func TestApplyVolumeS16MuteClears(t *testing.T) {
	buf := pcmS16([]int16{20000, -20000})
	applyVolumeS16(buf, 2, 0)
	for i, b := range buf {
		if b != 0 {
			t.Fatalf("muted byte %d = %d, want 0", i, b)
		}
	}
}

func TestApplyVolumeS16HalfAndBoost(t *testing.T) {
	buf := pcmS16([]int16{20000, -20000})
	applyVolumeS16(buf, 2, 50)
	got := samplesS16(buf)
	if got[0] != 10000 || got[1] != -10000 {
		t.Fatalf("half gain = %v, want [10000 -10000]", got)
	}

	buf = pcmS16([]int16{10000, -10000})
	applyVolumeS16(buf, 2, 200)
	got = samplesS16(buf)
	if got[0] != 20000 || got[1] != -20000 {
		t.Fatalf("2x gain = %v, want [20000 -20000]", got)
	}
}

func TestApplyVolumeS16ClipsBoost(t *testing.T) {
	buf := pcmS16([]int16{20000, -20000})
	applyVolumeS16(buf, 2, 400)
	got := samplesS16(buf)
	if got[0] != 32767 || got[1] != -32768 {
		t.Fatalf("clipped boost = %v, want [32767 -32768]", got)
	}
}

func TestSetVolumeClampsAndReadsBack(t *testing.T) {
	p := NewPlayer()
	if got := p.Volume(); got != 1 {
		t.Fatalf("default Volume() = %v, want 1", got)
	}
	p.SetVolume(0)
	if got := p.Volume(); got != 0 {
		t.Fatalf("muted Volume() = %v, want 0", got)
	}
	p.SetVolume(4)
	if got := p.Volume(); got != 4 {
		t.Fatalf("max Volume() = %v, want 4", got)
	}
	p.SetVolume(9)
	if got := p.Volume(); got != 4 {
		t.Fatalf("clamped Volume() = %v, want 4", got)
	}
	p.SetVolume(-1)
	if got := p.Volume(); got != 0 {
		t.Fatalf("negative Volume() = %v, want 0", got)
	}
}

func TestOnDataAppliesVolumeBeforeExpand(t *testing.T) {
	p := NewPlayer()
	p.channels = 2
	p.SetVolume(0.5)

	seg := newPlaybackSegment()
	seg.append([]int16{20000, -20000})
	seg.finishInput(nil)
	p.current = seg

	out := make([]byte, 2*2*2) // 2 frames stereo
	p.onData(out, nil, 2)

	l0 := int16(binary.LittleEndian.Uint16(out[0:]))
	r0 := int16(binary.LittleEndian.Uint16(out[2:]))
	l1 := int16(binary.LittleEndian.Uint16(out[4:]))
	r1 := int16(binary.LittleEndian.Uint16(out[6:]))
	if l0 != 10000 || r0 != 10000 {
		t.Fatalf("frame 0 L/R = %d/%d, want 10000/10000 (half gain, mono expand)", l0, r0)
	}
	if l1 != -10000 || r1 != -10000 {
		t.Fatalf("frame 1 L/R = %d/%d, want -10000/-10000", l1, r1)
	}
}

func TestApplyEngineVolumeIgnoresUnknown(t *testing.T) {
	ApplyEngineVolume(nil, 0.5) // must not panic
	p := NewPlayer()
	ApplyEngineVolume(p, 0.25)
	if got := p.Volume(); got != 0.25 {
		t.Fatalf("ApplyEngineVolume = %v, want 0.25", got)
	}
}

func pcmS16(samples []int16) []byte {
	buf := make([]byte, len(samples)*2)
	for i, s := range samples {
		binary.LittleEndian.PutUint16(buf[i*2:], uint16(s))
	}
	return buf
}

func samplesS16(buf []byte) []int16 {
	out := make([]int16, len(buf)/2)
	for i := range out {
		out[i] = int16(binary.LittleEndian.Uint16(buf[i*2:]))
	}
	return out
}
