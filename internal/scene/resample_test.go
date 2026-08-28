package scene

import (
	"math"
	"testing"
)

func TestSceneResampleLinearRejectsBadRates(t *testing.T) {
	cases := map[string]struct{ src, dst int }{
		"zero source":      {0, 24000},
		"zero destination": {24000, 0},
		"negative source":  {-24000, 24000},
		"negative dest":    {24000, -16000},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ResampleLinear(make([]float32, 8), tc.src, tc.dst); err == nil {
				t.Fatalf("ResampleLinear(%d, %d) accepted a non-positive rate", tc.src, tc.dst)
			}
		})
	}
}

// A matching rate must not touch the samples: the common single-rate cast pays
// nothing for a conversion it does not need.
func TestSceneResampleLinearIdentity(t *testing.T) {
	in := []float32{0.1, -0.2, 0.3}
	out, err := ResampleLinear(in, 24000, 24000)
	if err != nil {
		t.Fatalf("ResampleLinear: %v", err)
	}
	if len(out) != len(in) {
		t.Fatalf("length %d, want %d", len(out), len(in))
	}
	for i := range in {
		if out[i] != in[i] {
			t.Fatalf("sample %d = %v, want %v", i, out[i], in[i])
		}
	}
}

func TestSceneResampleLinearEmpty(t *testing.T) {
	out, err := ResampleLinear(nil, 24000, 16000)
	if err != nil {
		t.Fatalf("ResampleLinear: %v", err)
	}
	if len(out) != 0 {
		t.Fatalf("length %d, want 0", len(out))
	}
}

// The output length is the rounded ratio, not a truncated one. A truncating
// implementation passes a 3:2 case and loses a sample everywhere else, so the
// lengths are checked against math.Round rather than a hand-picked number.
func TestSceneResampleLinearLength(t *testing.T) {
	cases := map[string]struct {
		n        int
		src, dst int
	}{
		"24k to 16k":       {1000, 24000, 16000},
		"24k to 16k odd":   {1001, 24000, 16000},
		"16k to 24k":       {1000, 16000, 24000},
		"24k to 22.05k":    {997, 24000, 22050},
		"single sample up": {1, 16000, 24000},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			out, err := ResampleLinear(make([]float32, tc.n), tc.src, tc.dst)
			if err != nil {
				t.Fatalf("ResampleLinear: %v", err)
			}
			want := int(math.Round(float64(tc.n) * float64(tc.dst) / float64(tc.src)))
			if len(out) != want {
				t.Fatalf("length %d, want %d", len(out), want)
			}
		})
	}
}

// Downsampling a ramp keeps its shape: the first and last samples survive and
// the middle interpolates, which is what stops a converted turn from clicking
// where it is joined.
func TestSceneResampleLinearInterpolates(t *testing.T) {
	in := make([]float32, 24)
	for i := range in {
		in[i] = float32(i) / float32(len(in)-1)
	}
	out, err := ResampleLinear(in, 24000, 12000)
	if err != nil {
		t.Fatalf("ResampleLinear: %v", err)
	}
	if len(out) != 12 {
		t.Fatalf("length %d, want 12", len(out))
	}
	if out[0] != in[0] {
		t.Errorf("first sample = %v, want %v", out[0], in[0])
	}
	for i := 1; i < len(out); i++ {
		if out[i] <= out[i-1] {
			t.Fatalf("sample %d (%v) does not follow %v: the ramp was not preserved", i, out[i], out[i-1])
		}
	}
}
