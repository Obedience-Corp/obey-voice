//go:build integration

package onnxrt

import (
	"os"
	"path/filepath"
	"testing"
)

// audio8Dir locates the downloaded Audio8 snapshot; the test skips without it.
func audio8Dir(t *testing.T) string {
	t.Helper()
	dir := os.Getenv("AUDIO8_MODEL_DIR")
	if dir == "" {
		t.Skip("set AUDIO8_MODEL_DIR to run")
	}
	if _, err := os.Stat(filepath.Join(dir, "codec_decoder_fp16.onnx")); err != nil {
		t.Skipf("no codec graph in %s: %v", dir, err)
	}
	return dir
}

func TestRunCodecDecoder(t *testing.T) {
	dir := audio8Dir(t)
	env, err := NewEnv()
	if err != nil {
		t.Fatalf("NewEnv: %v", err)
	}
	defer env.Close()

	sess, err := NewSession(env, filepath.Join(dir, "codec_decoder_fp16.onnx"), 4, 2, []string{"audio"})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	const frames = 8
	codes := make([]int64, 10*frames)
	for i := range codes {
		codes[i] = int64(i % 4096)
	}
	out, err := sess.Run(map[string]*Tensor{"codes": I64Tensor(codes, 1, 10, frames)})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("want 1 output, got %d", len(out))
	}
	// 2048 samples per codec frame.
	if want := frames * 2048; len(out[0].Data) != want {
		t.Fatalf("audio samples = %d, want %d (shape %v)", len(out[0].Data), want, out[0].Shape)
	}
	t.Logf("decoded %d frames -> %d samples, shape %v", frames, len(out[0].Data), out[0].Shape)
}
