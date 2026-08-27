//go:build !cgo

// Stub build. The container integration job builds the CLI with CGO_ENABLED=0,
// and the cgo implementation blank-imports sherpa-onnx-go for its link flags -
// which would drag a cgo-only module into a cgo-free build. These stubs keep the
// package importable there; any actual use reports that it needs cgo.
package onnxrt

import "errors"

// ErrNoCGO is returned by every entry point in a cgo-free build.
var ErrNoCGO = errors.New("onnxrt: ONNX Runtime requires a cgo-enabled build")

// Env is a stub environment.
type Env struct{}

// NewEnv always fails without cgo.
func NewEnv() (*Env, error) { return nil, ErrNoCGO }

// Close is a no-op.
func (e *Env) Close() {}

// Session is a stub session.
type Session struct{}

// NewSession always fails without cgo.
func NewSession(*Env, string, int, int, []string) (*Session, error) { return nil, ErrNoCGO }

// Close is a no-op.
func (s *Session) Close() {}

// Run always fails without cgo.
func (s *Session) Run(map[string]*Tensor) ([]Output, error) { return nil, ErrNoCGO }

// Tensor mirrors the cgo build's tensor type.
type Tensor struct {
	F32   []float32
	I64   []int64
	Bool  []bool
	Shape []int64
}

// F32Tensor builds a float32 tensor.
func F32Tensor(data []float32, shape ...int64) *Tensor { return &Tensor{F32: data, Shape: shape} }

// I64Tensor builds an int64 tensor.
func I64Tensor(data []int64, shape ...int64) *Tensor { return &Tensor{I64: data, Shape: shape} }

// BoolTensor builds a bool tensor.
func BoolTensor(data []bool, shape ...int64) *Tensor { return &Tensor{Bool: data, Shape: shape} }

// Output mirrors the cgo build's output type.
type Output struct {
	Data  []float32
	Shape []int64
}
