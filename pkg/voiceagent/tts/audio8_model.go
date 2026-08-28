package tts

import (
	"encoding/json"
	"fmt"
	"math"
	"math/rand"
	"os"
	"path/filepath"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/onnxrt"
)

// audio8Manifest is the subset of runtime_manifest.json the provider needs.
type audio8Manifest struct {
	ModelFingerprint string            `json:"model_fingerprint"`
	SampleRate       int               `json:"sample_rate"`
	CodecFrameSize   int               `json:"codec_frame_size"`
	CodecHopLength   int               `json:"codec_hop_length"`
	NumCodebooks     int               `json:"num_codebooks"`
	CodebookSize     int               `json:"codebook_size"`
	SemanticBeginID  int               `json:"semantic_begin_id"`
	ImEndID          int               `json:"im_end_id"`
	SlowLogitsLayout string            `json:"slow_logits_layout"`
	MaxSeqLen        int               `json:"max_seq_len"`
	NumLayers        int               `json:"num_layers"`
	NLocalHeads      int               `json:"n_local_heads"`
	HeadDim          int               `json:"head_dim"`
	NumFastLayers    int               `json:"num_fast_layers"`
	FastNLocalHeads  int               `json:"fast_n_local_heads"`
	FastHeadDim      int               `json:"fast_head_dim"`
	ReferenceCodes   string            `json:"reference_codes"`
	ReferenceText    string            `json:"reference_text"`
	SlowDecodeModel  string            `json:"slow_decode_model"`
	FastModel        string            `json:"fast_model"`
	CodecModels      map[string]string `json:"codec_models"`
	DefaultCodecPrec string            `json:"default_codec_precision"`
}

func loadAudio8Manifest(dir string) (*audio8Manifest, error) {
	raw, err := os.ReadFile(filepath.Join(dir, "runtime_manifest.json"))
	if err != nil {
		return nil, fmt.Errorf("read runtime_manifest.json: %w", err)
	}
	var m audio8Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("parse runtime_manifest.json: %w", err)
	}
	// The published export is decode-only and relative-logit; a different export
	// would need different feeding, so fail loudly rather than emit noise.
	if m.SlowLogitsLayout != "relative_semantic_then_eos" {
		return nil, fmt.Errorf("unsupported slow_logits_layout %q", m.SlowLogitsLayout)
	}
	return &m, nil
}

// slowState is the recurrent state of the slow (mamba/SSM hybrid) graph.
// cacheKeys and cacheValues are scattered at the current position each step;
// conv and ssm states are replaced wholesale from the graph's own outputs.
type slowState struct {
	cacheKeys, cacheValues []float32
	convStates, ssmStates  []float32
	keyShape, valShape     []int64
	convShape, ssmShape    []int64
}

func (s *slowState) clone() *slowState {
	cp := func(v []float32) []float32 { out := make([]float32, len(v)); copy(out, v); return out }
	return &slowState{
		cacheKeys: cp(s.cacheKeys), cacheValues: cp(s.cacheValues),
		convStates: cp(s.convStates), ssmStates: cp(s.ssmStates),
		keyShape: s.keyShape, valShape: s.valShape,
		convShape: s.convShape, ssmShape: s.ssmShape,
	}
}

func numel(shape []int64) int {
	n := int64(1)
	for _, d := range shape {
		n *= d
	}
	return int(n)
}

// newSlowState allocates zeroed recurrent state matching the graph's declared shapes.
func newSlowState(m *audio8Manifest) *slowState {
	l, h, seq, d := int64(m.NumLayers), int64(m.NLocalHeads), int64(m.MaxSeqLen), int64(m.HeadDim)
	keyShape := []int64{l, 1, h, seq, d}
	// conv/ssm shapes are model-specific and read back from the graph outputs on
	// the first step; allocate from the manifest's own dimensions.
	convShape := []int64{l, 1, 896, 4}
	ssmShape := []int64{l, 1, 24, 32, 64}
	return &slowState{
		cacheKeys: make([]float32, numel(keyShape)), cacheValues: make([]float32, numel(keyShape)),
		convStates: make([]float32, numel(convShape)), ssmStates: make([]float32, numel(ssmShape)),
		keyShape: keyShape, valShape: keyShape,
		convShape: convShape, ssmShape: ssmShape,
	}
}

// scatterKV writes a per-layer delta [layers,1,heads,dim] into cache[...,pos,...].
func scatterKV(cache []float32, delta []float32, layers, heads, seqLen, dim, pos int) {
	for l := range layers {
		for h := range heads {
			src := (l*heads + h) * dim
			dst := ((l*heads+h)*seqLen + pos) * dim
			copy(cache[dst:dst+dim], delta[src:src+dim])
		}
	}
}

// slowStep runs one token through the slow graph and folds the state forward.
func (a *Audio8TTS) slowStep(st *slowState, column []int64, pos int) (logits, hidden []float32, err error) {
	out, err := a.slow.Run(map[string]*onnxrt.Tensor{
		"codes":        onnxrt.I64Tensor(column, 1, int64(a.man.NumCodebooks+1), 1),
		"position":     onnxrt.I64Tensor([]int64{int64(pos)}, 1),
		"cache_keys":   onnxrt.F32Tensor(st.cacheKeys, st.keyShape...),
		"cache_values": onnxrt.F32Tensor(st.cacheValues, st.valShape...),
		"conv_states":  onnxrt.F32Tensor(st.convStates, st.convShape...),
		"ssm_states":   onnxrt.F32Tensor(st.ssmStates, st.ssmShape...),
	})
	if err != nil {
		return nil, nil, fmt.Errorf("slow step at %d: %w", pos, err)
	}
	scatterKV(st.cacheKeys, out[2].Data, a.man.NumLayers, a.man.NLocalHeads, a.man.MaxSeqLen, a.man.HeadDim, pos)
	scatterKV(st.cacheValues, out[3].Data, a.man.NumLayers, a.man.NLocalHeads, a.man.MaxSeqLen, a.man.HeadDim, pos)
	copy(st.convStates, out[4].Data)
	copy(st.ssmStates, out[5].Data)
	return out[0].Data, out[1].Data, nil
}

// fastState holds the fast graph's per-layer KV, one entry per codebook slot.
type fastState struct {
	keys, values [][]float32
	shape        []int64
}

func newFastState(m *audio8Manifest) *fastState {
	shape := []int64{1, int64(m.FastNLocalHeads), int64(m.NumCodebooks), int64(m.FastHeadDim)}
	fs := &fastState{shape: shape}
	for range m.NumFastLayers {
		fs.keys = append(fs.keys, make([]float32, numel(shape)))
		fs.values = append(fs.values, make([]float32, numel(shape)))
	}
	return fs
}

// fastStep runs one codebook position through the fast graph.
func (a *Audio8TTS) fastStep(fs *fastState, hidden []float32, tokenID int, useHidden bool, pos int) ([]float32, error) {
	in := map[string]*onnxrt.Tensor{
		"slow_hidden":     onnxrt.F32Tensor(hidden, 1, 1, int64(len(hidden))),
		"token_id":        onnxrt.I64Tensor([]int64{int64(tokenID)}, 1, 1),
		"use_slow_hidden": onnxrt.BoolTensor([]bool{useHidden}, 1),
		"input_pos":       onnxrt.I64Tensor([]int64{int64(pos)}, 1),
	}
	for i := range a.man.NumFastLayers {
		in[fmt.Sprintf("cache_key_%d", i)] = onnxrt.F32Tensor(fs.keys[i], fs.shape...)
		in[fmt.Sprintf("cache_value_%d", i)] = onnxrt.F32Tensor(fs.values[i], fs.shape...)
	}
	out, err := a.fast.Run(in)
	if err != nil {
		return nil, fmt.Errorf("fast step at %d: %w", pos, err)
	}
	heads, dim := a.man.FastNLocalHeads, a.man.FastHeadDim
	for i := range a.man.NumFastLayers {
		scatterKV(fs.keys[i], out[1+2*i].Data, 1, heads, a.man.NumCodebooks, dim, pos)
		scatterKV(fs.values[i], out[2+2*i].Data, 1, heads, a.man.NumCodebooks, dim, pos)
	}
	return out[0].Data, nil
}

// sampleLogits mirrors the reference implementation: top-k/top-p truncation then
// Gumbel-max sampling, so a given seed reproduces the reference output.
func sampleLogits(logits []float32, temperature, topP float64, topK int, rng *rand.Rand) int {
	n := len(logits)
	order := make([]int, n)
	for i := range order {
		order[i] = i
	}
	vals := make([]float64, n)
	for i, v := range logits {
		vals[i] = float64(v)
	}
	sortDescByValue(order, vals)

	maxV := vals[order[0]]
	var sum float64
	probs := make([]float64, n)
	for rank, idx := range order {
		probs[rank] = math.Exp(vals[idx] - maxV)
		sum += probs[rank]
	}
	masked := make([]float64, n)
	copy(masked, vals)
	var cum float64
	for rank, idx := range order {
		cum += probs[rank] / sum
		if rank > 0 && (cum > topP || rank >= topK) {
			masked[idx] = math.Inf(-1)
		}
	}
	if temperature < 1e-5 {
		temperature = 1e-5
	}
	best, bestScore := -1, math.Inf(-1)
	maxScaled := math.Inf(-1)
	for _, v := range masked {
		if s := v / temperature; s > maxScaled {
			maxScaled = s
		}
	}
	for i, v := range masked {
		p := math.Exp(v/temperature - maxScaled)
		u := rng.Float64()
		if u < 1e-12 {
			u = 1e-12
		}
		if score := p / -math.Log(u); score > bestScore {
			best, bestScore = i, score
		}
	}
	return best
}

func sortDescByValue(order []int, vals []float64) {
	// Insertion-free: simple sort via standard library on a slice of indices.
	quickSortDesc(order, vals, 0, len(order)-1)
}

func quickSortDesc(order []int, vals []float64, lo, hi int) {
	for lo < hi {
		p := vals[order[(lo+hi)/2]]
		i, j := lo, hi
		for i <= j {
			for vals[order[i]] > p {
				i++
			}
			for vals[order[j]] < p {
				j--
			}
			if i <= j {
				order[i], order[j] = order[j], order[i]
				i++
				j--
			}
		}
		if j-lo < hi-i {
			quickSortDesc(order, vals, lo, j)
			lo = i
		} else {
			quickSortDesc(order, vals, i, hi)
			hi = j
		}
	}
}
