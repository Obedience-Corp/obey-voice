package onnxrt

import "math"

// Tensor is a dense tensor backed by Go memory. The backing slice must outlive
// any Run call it is passed to; callers keep the slices alive across the call.
//
// Exactly one payload field is set. It lives here rather than beside either
// build's Session so the cgo and cgo-free builds cannot describe different
// tensors.
type Tensor struct {
	F32   []float32
	F16   []uint16 // IEEE-754 binary16 bit patterns
	I64   []int64
	Bool  []bool
	Shape []int64
}

// F32Tensor builds a float32 tensor.
func F32Tensor(data []float32, shape ...int64) *Tensor { return &Tensor{F32: data, Shape: shape} }

// F16Tensor builds a float16 tensor from float32 samples, converting as it
// copies. ONNX Runtime does not coerce input types, so a graph declaring a
// float16 input — the Audio8 codec encoder does — has to be fed halves.
func F16Tensor(data []float32, shape ...int64) *Tensor {
	half := make([]uint16, len(data))
	for i, v := range data {
		half[i] = Float32ToFloat16(v)
	}
	return &Tensor{F16: half, Shape: shape}
}

// I64Tensor builds an int64 tensor.
func I64Tensor(data []int64, shape ...int64) *Tensor { return &Tensor{I64: data, Shape: shape} }

// BoolTensor builds a bool tensor.
func BoolTensor(data []bool, shape ...int64) *Tensor { return &Tensor{Bool: data, Shape: shape} }

// Output is a result tensor copied out of the runtime. The field carrying the
// values is the one matching the graph's declared element type: Data for
// float32 outputs, I64 for int64 ones. Run rejects any other type rather than
// reinterpreting its bytes.
type Output struct {
	Data  []float32
	I64   []int64
	Shape []int64
}

// Float32ToFloat16 converts to IEEE-754 binary16 with round-to-nearest-even,
// flushing overflow to infinity and preserving NaN and signed zero.
func Float32ToFloat16(f float32) uint16 {
	bits := math.Float32bits(f)
	sign := uint16(bits>>16) & 0x8000
	exp := int32(bits>>23) & 0xff
	mant := bits & 0x7fffff

	if exp == 0xff { // Inf or NaN
		if mant != 0 {
			return sign | 0x7e00 // quiet NaN
		}
		return sign | 0x7c00
	}

	// Half exponent field: unbias by 127, rebias by 15.
	e := exp - 112
	if e >= 0x1f {
		return sign | 0x7c00 // overflows the half range
	}
	if e >= 1 { // normal
		half := uint32(e)<<10 | mant>>13
		return sign | uint16(half+roundIncrement(mant, 13))
	}

	// Subnormal: the value is mant24 * 2^(exp-150) and the smallest half step
	// is 2^-24, so the mantissa is mant24 shifted right by 14-e places. Every
	// float32 subnormal (exp 0) lands past the 24-place cut-off and flushes to
	// zero here, which is why the implicit leading one is unconditional.
	shift := uint32(14 - e)
	if shift > 24 {
		return sign // rounds to zero
	}
	mant24 := mant | 0x800000
	// A carry out of the mantissa lands exactly on the smallest normal half,
	// which is the correct rounding, so no special case is needed.
	return sign | uint16((mant24>>shift)+roundIncrement(mant24, shift))
}

// roundIncrement is 1 when dropping the low `shift` bits of mant rounds up
// under round-to-nearest-even, and 0 otherwise.
func roundIncrement(mant, shift uint32) uint32 {
	if shift == 0 || shift > 31 {
		return 0
	}
	half := uint32(1) << (shift - 1)
	dropped := mant & (1<<shift - 1)
	if dropped > half || (dropped == half && (mant>>shift)&1 == 1) {
		return 1
	}
	return 0
}
