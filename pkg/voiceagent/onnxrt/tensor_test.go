package onnxrt

import (
	"math"
	"testing"
)

// TestFloat32ToFloat16 pins the conversion against hand-computed IEEE-754
// binary16 patterns. The codec encoder declares a float16 input, so a wrong
// mantissa here is a quietly distorted reference voice, not a build failure.
func TestFloat32ToFloat16(t *testing.T) {
	tests := []struct {
		name string
		in   float32
		want uint16
	}{
		// Errors and edges first.
		{"positive infinity", float32(math.Inf(1)), 0x7c00},
		{"negative infinity", float32(math.Inf(-1)), 0xfc00},
		{"nan stays nan", float32(math.NaN()), 0x7e00},
		{"overflow becomes infinity", 70000, 0x7c00},
		{"largest finite half", 65504, 0x7bff},
		{"just past the largest half rounds to infinity", 65520, 0x7c00},
		{"float32 subnormal flushes to zero", math.SmallestNonzeroFloat32, 0x0000},
		{"underflow keeps the sign", -1e-9, 0x8000},

		{"zero", 0, 0x0000},
		{"negative zero", float32(math.Copysign(0, -1)), 0x8000},
		{"one", 1, 0x3c00},
		{"minus two", -2, 0xc000},
		{"one half", 0.5, 0x3800},
		{"smallest normal half", 6.103515625e-05, 0x0400},
		{"smallest subnormal half", 5.960464477539063e-08, 0x0001},
		{"largest subnormal half", 6.097555160522461e-05, 0x03ff},
		// Half spacing is 2 around 2048, so 2049 and 2051 are both exact ties;
		// round-to-nearest-even takes 2048 and 2052 respectively.
		{"a tie rounds down to the even neighbour", 2049, 0x6800},
		{"a tie rounds up to the even neighbour", 2051, 0x6802},
		{"a near miss rounds to the odd neighbour", 2050.9, 0x6801},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Float32ToFloat16(tt.in); got != tt.want {
				t.Errorf("Float32ToFloat16(%g) = %#04x, want %#04x", tt.in, got, tt.want)
			}
		})
	}
}

// TestF16TensorConverts checks the constructor produces halves and a shape,
// since nothing else in the package can read a float16 payload back.
func TestF16TensorConverts(t *testing.T) {
	tensor := F16Tensor([]float32{0, 1, -2}, 1, 1, 3)
	if tensor.F32 != nil || tensor.I64 != nil || tensor.Bool != nil {
		t.Fatalf("F16Tensor set a second payload field: %+v", tensor)
	}
	want := []uint16{0x0000, 0x3c00, 0xc000}
	if len(tensor.F16) != len(want) {
		t.Fatalf("F16 length = %d, want %d", len(tensor.F16), len(want))
	}
	for i, w := range want {
		if tensor.F16[i] != w {
			t.Errorf("F16[%d] = %#04x, want %#04x", i, tensor.F16[i], w)
		}
	}
	if len(tensor.Shape) != 3 || tensor.Shape[2] != 3 {
		t.Errorf("Shape = %v, want [1 1 3]", tensor.Shape)
	}
}
