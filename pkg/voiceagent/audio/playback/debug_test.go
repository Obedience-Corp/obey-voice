package playback

import (
	"encoding/binary"
	"encoding/json"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"math"
	"os"
	"path/filepath"
	"testing"
)

func TestPlayerDebugRecorderCapturesSourceDeviceAndTiming(t *testing.T) {
	root := t.TempDir()
	recorder, err := newPlayerDebugRecorder(root, "Test Speakers", 24_000, 44_100, 1)
	if err != nil {
		t.Fatalf("newPlayerDebugRecorder() error = %v", err)
	}

	recorder.captureSource(24_000, []float32{0.25, -0.25})
	callback := make([]byte, 4)
	positive := int16(math.MaxInt16 / 2)
	negative := -positive
	binary.LittleEndian.PutUint16(callback[0:2], uint16(positive))
	binary.LittleEndian.PutUint16(callback[2:4], uint16(negative))
	recorder.captureCallback(callback, 2, 2)
	recorder.close()

	source, sourceRate, err := audio.ReadWAVFloat32(filepath.Join(recorder.dir, "source-0001-24000hz.wav"))
	if err != nil {
		t.Fatalf("ReadWAVFloat32(source) error = %v", err)
	}
	if sourceRate != 24_000 || len(source) != 2 {
		t.Fatalf("source = %d Hz/%d samples, want 24000 Hz/2 samples", sourceRate, len(source))
	}
	device, deviceRate, err := audio.ReadWAVFloat32(filepath.Join(recorder.dir, "device-output.wav"))
	if err != nil {
		t.Fatalf("ReadWAVFloat32(device) error = %v", err)
	}
	if deviceRate != 44_100 || len(device) != 2 {
		t.Fatalf("device = %d Hz/%d samples, want 44100 Hz/2 samples", deviceRate, len(device))
	}

	encoded, err := os.ReadFile(filepath.Join(recorder.dir, "callbacks.jsonl"))
	if err != nil {
		t.Fatalf("read callback metadata: %v", err)
	}
	var callbackMeta debugCallbackMetadata
	if err := json.Unmarshal(encoded, &callbackMeta); err != nil {
		t.Fatalf("decode callback metadata: %v", err)
	}
	if callbackMeta.RequestedFrames != 2 || callbackMeta.WrittenFrames != 2 || callbackMeta.SilenceFrames != 0 {
		t.Fatalf("callback metadata = %+v", callbackMeta)
	}
}
