// Package onnxrt is a minimal CGO binding to the ONNX Runtime C API.
//
// The shared library is not vendored here: obey-voice already links
// libonnxruntime via sherpa-onnx-go, whose cgo LDFLAGS carry -lonnxruntime and
// the -L/-rpath for its platform directory. This package blank-imports that
// module so those flags reach any binary using onnxrt - declaring -lonnxruntime
// again here only produces a duplicate-library warning at link time. It pins
// ORT_API_VERSION to the header vendored under include/ (1.27.0) so GetApi never
// asks the library for a newer contract than it implements.
//go:build cgo

package onnxrt

/*
#cgo CFLAGS: -I${SRCDIR}/include
#include <stdlib.h>
#include "onnxruntime_c_api.h"

static const OrtApi* ort_api(void) {
  static const OrtApi* api = NULL;
  if (api == NULL) api = OrtGetApiBase()->GetApi(ORT_API_VERSION);
  return api;
}
static const char* ort_err(OrtStatus* s) { return ort_api()->GetErrorMessage(s); }
static void ort_status_free(OrtStatus* s) { ort_api()->ReleaseStatus(s); }

static OrtStatus* ort_env_new(OrtEnv** out) {
  return ort_api()->CreateEnv(ORT_LOGGING_LEVEL_ERROR, "obey-voice", out);
}
static void ort_env_free(OrtEnv* e) { ort_api()->ReleaseEnv(e); }

static OrtStatus* ort_session_new(OrtEnv* env, const char* path, int intra, int inter, OrtSession** out) {
  OrtSessionOptions* opt = NULL;
  OrtStatus* st = ort_api()->CreateSessionOptions(&opt);
  if (st) return st;
  if (intra > 0) { st = ort_api()->SetIntraOpNumThreads(opt, intra); if (st) { ort_api()->ReleaseSessionOptions(opt); return st; } }
  if (inter > 0) { st = ort_api()->SetInterOpNumThreads(opt, inter); if (st) { ort_api()->ReleaseSessionOptions(opt); return st; } }
  st = ort_api()->CreateSession(env, path, opt, out);
  ort_api()->ReleaseSessionOptions(opt);
  return st;
}
static void ort_session_free(OrtSession* s) { ort_api()->ReleaseSession(s); }

static OrtStatus* ort_meminfo_new(OrtMemoryInfo** out) {
  return ort_api()->CreateCpuMemoryInfo(OrtArenaAllocator, OrtMemTypeDefault, out);
}
static void ort_meminfo_free(OrtMemoryInfo* m) { ort_api()->ReleaseMemoryInfo(m); }

static OrtStatus* ort_tensor_new(OrtMemoryInfo* mi, void* data, size_t nbytes,
                                 const int64_t* shape, size_t ndim,
                                 ONNXTensorElementDataType dt, OrtValue** out) {
  return ort_api()->CreateTensorWithDataAsOrtValue(mi, data, nbytes, shape, ndim, dt, out);
}
static void ort_value_free(OrtValue* v) { ort_api()->ReleaseValue(v); }

static OrtStatus* ort_run(OrtSession* s, const char* const* in_names, const OrtValue* const* ins, size_t n_in,
                          const char* const* out_names, size_t n_out, OrtValue** outs) {
  return ort_api()->Run(s, NULL, in_names, ins, n_in, out_names, n_out, outs);
}
static OrtStatus* ort_tensor_data(OrtValue* v, void** out) { return ort_api()->GetTensorMutableData(v, out); }

static OrtStatus* ort_tensor_shape(OrtValue* v, int64_t* dims, size_t cap, size_t* ndim) {
  OrtTensorTypeAndShapeInfo* info = NULL;
  OrtStatus* st = ort_api()->GetTensorTypeAndShape(v, &info);
  if (st) return st;
  size_t n = 0;
  st = ort_api()->GetDimensionsCount(info, &n);
  if (st) { ort_api()->ReleaseTensorTypeAndShapeInfo(info); return st; }
  if (n > cap) n = cap;
  st = ort_api()->GetDimensions(info, dims, n);
  *ndim = n;
  ort_api()->ReleaseTensorTypeAndShapeInfo(info);
  return st;
}
*/
import "C"

import (
	"fmt"
	"runtime"
	"unsafe"

	_ "github.com/k2-fsa/sherpa-onnx-go/sherpa_onnx" // provides libonnxruntime and its -L/-rpath
)

const maxDims = 8

func check(st *C.OrtStatus) error {
	if st == nil {
		return nil
	}
	msg := C.GoString(C.ort_err(st))
	C.ort_status_free(st)
	return fmt.Errorf("onnxruntime: %s", msg)
}

// Env is a process-wide ONNX Runtime environment.
type Env struct {
	ptr *C.OrtEnv
	mem *C.OrtMemoryInfo
}

// NewEnv creates the runtime environment and a CPU memory descriptor.
func NewEnv() (*Env, error) {
	e := &Env{}
	if err := check(C.ort_env_new(&e.ptr)); err != nil {
		return nil, err
	}
	if err := check(C.ort_meminfo_new(&e.mem)); err != nil {
		C.ort_env_free(e.ptr)
		return nil, err
	}
	return e, nil
}

// Close releases the environment.
func (e *Env) Close() {
	if e.mem != nil {
		C.ort_meminfo_free(e.mem)
		e.mem = nil
	}
	if e.ptr != nil {
		C.ort_env_free(e.ptr)
		e.ptr = nil
	}
}

// Session is a loaded ONNX graph.
type Session struct {
	env      *Env
	ptr      *C.OrtSession
	outNames []*C.char
}

// NewSession loads modelPath. intra and inter set the op thread counts; the two
// graph families in a DualAR model want very different values, so they are
// per-session rather than global.
func NewSession(env *Env, modelPath string, intra, inter int, outputs []string) (*Session, error) {
	cPath := C.CString(modelPath)
	defer C.free(unsafe.Pointer(cPath))
	s := &Session{env: env}
	if err := check(C.ort_session_new(env.ptr, cPath, C.int(intra), C.int(inter), &s.ptr)); err != nil {
		return nil, fmt.Errorf("load %s: %w", modelPath, err)
	}
	for _, n := range outputs {
		s.outNames = append(s.outNames, C.CString(n))
	}
	return s, nil
}

// Close releases the session.
func (s *Session) Close() {
	for _, n := range s.outNames {
		C.free(unsafe.Pointer(n))
	}
	s.outNames = nil
	if s.ptr != nil {
		C.ort_session_free(s.ptr)
		s.ptr = nil
	}
}

// Tensor is a dense tensor backed by Go memory. The backing slice must outlive
// any Run call it is passed to; callers keep the slices alive across the call.
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

func (t *Tensor) toOrt(env *Env) (*C.OrtValue, error) {
	var (
		ptr    unsafe.Pointer
		nbytes C.size_t
		dt     C.ONNXTensorElementDataType
	)
	switch {
	case t.F32 != nil:
		ptr, nbytes, dt = unsafe.Pointer(&t.F32[0]), C.size_t(len(t.F32)*4), C.ONNX_TENSOR_ELEMENT_DATA_TYPE_FLOAT
	case t.I64 != nil:
		ptr, nbytes, dt = unsafe.Pointer(&t.I64[0]), C.size_t(len(t.I64)*8), C.ONNX_TENSOR_ELEMENT_DATA_TYPE_INT64
	case t.Bool != nil:
		ptr, nbytes, dt = unsafe.Pointer(&t.Bool[0]), C.size_t(len(t.Bool)), C.ONNX_TENSOR_ELEMENT_DATA_TYPE_BOOL
	default:
		return nil, fmt.Errorf("onnxrt: empty tensor")
	}
	shape := t.Shape
	if len(shape) == 0 {
		return nil, fmt.Errorf("onnxrt: tensor needs a shape")
	}
	var v *C.OrtValue
	err := check(C.ort_tensor_new(env.mem, ptr, nbytes,
		(*C.int64_t)(unsafe.Pointer(&shape[0])), C.size_t(len(shape)), dt, &v))
	if err != nil {
		return nil, err
	}
	return v, nil
}

// Output is a float32 result tensor copied out of the runtime.
type Output struct {
	Data  []float32
	Shape []int64
}

// Run executes the graph. inputs maps graph input names to tensors; outputs come
// back in the order given to NewSession.
func (s *Session) Run(inputs map[string]*Tensor) ([]Output, error) {
	names := make([]*C.char, 0, len(inputs))
	vals := make([]*C.OrtValue, 0, len(inputs))
	defer func() {
		for _, n := range names {
			C.free(unsafe.Pointer(n))
		}
		for _, v := range vals {
			if v != nil {
				C.ort_value_free(v)
			}
		}
	}()
	for name, t := range inputs {
		v, err := t.toOrt(s.env)
		if err != nil {
			return nil, fmt.Errorf("input %s: %w", name, err)
		}
		names = append(names, C.CString(name))
		vals = append(vals, v)
	}

	outs := make([]*C.OrtValue, len(s.outNames))
	err := check(C.ort_run(s.ptr,
		(**C.char)(unsafe.Pointer(&names[0])),
		(**C.OrtValue)(unsafe.Pointer(&vals[0])), C.size_t(len(vals)),
		(**C.char)(unsafe.Pointer(&s.outNames[0])), C.size_t(len(s.outNames)),
		(**C.OrtValue)(unsafe.Pointer(&outs[0]))))
	// Inputs are only borrowed for the duration of Run; keep the Go backing
	// arrays alive until it returns.
	runtime.KeepAlive(inputs)
	if err != nil {
		return nil, err
	}

	result := make([]Output, len(outs))
	for i, ov := range outs {
		if ov == nil {
			continue
		}
		dims := make([]C.int64_t, maxDims)
		var ndim C.size_t
		if err := check(C.ort_tensor_shape(ov, &dims[0], C.size_t(maxDims), &ndim)); err != nil {
			C.ort_value_free(ov)
			return nil, err
		}
		shape := make([]int64, int(ndim))
		n := int64(1)
		for d := 0; d < int(ndim); d++ {
			shape[d] = int64(dims[d])
			n *= shape[d]
		}
		var raw unsafe.Pointer
		if err := check(C.ort_tensor_data(ov, &raw)); err != nil {
			C.ort_value_free(ov)
			return nil, err
		}
		// Copy out: the OrtValue owns this memory and is released below.
		src := unsafe.Slice((*float32)(raw), int(n))
		data := make([]float32, int(n))
		copy(data, src)
		C.ort_value_free(ov)
		result[i] = Output{Data: data, Shape: shape}
	}
	return result, nil
}
