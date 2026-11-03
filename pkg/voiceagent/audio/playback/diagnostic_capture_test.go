package playback

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestDiagnosticCaptureLimitsAndConcurrentStop(t *testing.T) {
	r, err := newPlayerDebugRecorder(t.TempDir(), "test", 24000, 24000, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !r.status().Active {
		t.Fatal("capture should be visibly active")
	}
	r.bytes.Store(diagnosticMaxBytes - 2)
	r.captureCallback([]byte{1, 2, 3, 4}, 2, 2)
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() { defer wg.Done(); r.captureCallback([]byte{0, 0}, 1, 1); r.close() }()
	}
	wg.Wait()
	status := r.status()
	if status.Active || status.Bytes > diagnosticMaxBytes || status.DroppedEvents == 0 {
		t.Fatalf("unbounded status: %+v", status)
	}
	if _, err := os.Stat(filepath.Join(r.dir, "summary.json")); err != nil {
		t.Fatal(err)
	}
}

func TestDiagnosticCaptureRetentionAndExpiry(t *testing.T) {
	root := t.TempDir()
	unrelated := filepath.Join(root, "user-files")
	if err := os.Mkdir(unrelated, 0700); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		r, err := newPlayerDebugRecorder(root, "test", 24000, 24000, 1)
		if err != nil {
			t.Fatal(err)
		}
		r.close()
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 4 {
		t.Fatalf("retained %d entries; want 3 captures and user directory", len(entries))
	}
	for _, e := range entries {
		if e.Name() != "user-files" {
			old := time.Now().Add(-2 * diagnosticRetention)
			if err := os.Chtimes(filepath.Join(root, e.Name()), old, old); err != nil {
				t.Fatal(err)
			}
		}
	}
	pruneDiagnosticCaptures(root)
	entries, err = os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "user-files" {
		t.Fatalf("expiry removed wrong entries: %v", entries)
	}
}

func TestDiagnosticCapturePlayerAPIWithoutHardware(t *testing.T) {
	p := &Player{debugRoot: t.TempDir()}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := p.StartDiagnosticCapture(ctx); err != context.Canceled {
		t.Fatalf("canceled start: %v", err)
	}
	status, err := p.StartDiagnosticCapture(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !status.Active || status.ExpiresAt.Sub(status.StartedAt) != time.Minute {
		t.Fatalf("status: %+v", status)
	}
	again, err := p.StartDiagnosticCapture(context.Background())
	if err != nil || again.Directory != status.Directory {
		t.Fatalf("duplicate start: %+v, %v", again, err)
	}
	stopped := p.StopDiagnosticCapture()
	if stopped.Active || stopped.LimitReason != "stopped" {
		t.Fatalf("stop status: %+v", stopped)
	}
	if p.DiagnosticCaptureStatus().Directory != status.Directory {
		t.Fatal("stop lost status")
	}
}

func TestDiagnosticCaptureDuration(t *testing.T) {
	r, err := newPlayerDebugRecorder(t.TempDir(), "test", 24000, 24000, 1)
	if err != nil {
		t.Fatal(err)
	}
	r.timer.Reset(time.Millisecond)
	deadline := time.Now().Add(time.Second)
	for r.status().Active && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	r.close()
	if s := r.status(); s.Active || s.LimitReason != "duration_limit" {
		t.Fatalf("duration failed: %+v", s)
	}

}

func TestDiagnosticCaptureLazyOpenUsesNegotiatedFormat(t *testing.T) {
	r, err := newPlayerDebugRecorder(t.TempDir(), "pending", 24000, 24000, 1)
	if err != nil {
		t.Fatal(err)
	}
	r.configureFormat("actual", 24000, 48000, 2)
	r.captureCallback(make([]byte, 16), 4, 4)
	r.close()
	data, err := os.ReadFile(filepath.Join(r.dir, "device-output.wav"))
	if err != nil {
		t.Fatal(err)
	}
	if binary.LittleEndian.Uint32(data[24:28]) != 48000 || binary.LittleEndian.Uint16(data[22:24]) != 2 {
		t.Fatal("WAV retained placeholder format")
	}
	data, err = os.ReadFile(filepath.Join(r.dir, "metadata.json"))
	if err != nil {
		t.Fatal(err)
	}
	var meta debugAudioMetadata
	if err := json.Unmarshal(data, &meta); err != nil {
		t.Fatal(err)
	}
	if meta.DeviceSampleRate != 48000 || meta.Channels != 2 || meta.DeviceName != "actual" {
		t.Fatalf("metadata: %+v", meta)
	}
}

func TestDiagnosticCaptureRetainsIncrementalPrefixOnStopAndReset(t *testing.T) {
	for _, reset := range []bool{false, true} {
		name := "stop"
		if reset {
			name = "reset"
		}
		t.Run(name, func(t *testing.T) {
			const rate = 24000
			p := &Player{debugRoot: t.TempDir(), sampleRate: rate, clientRate: rate, channels: 1}
			p.volumeHundredths.Store(100)
			status, err := p.StartDiagnosticCapture(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			stream := audio.NewPCMStream(ctx)
			stream.SetIncremental(true)
			if err := stream.SetSampleRate(rate); err != nil {
				t.Fatal(err)
			}
			segment := newPlaybackSegment()
			done := make(chan struct{})
			go func() { defer close(done); pumpIncremental(ctx, segment, stream, p.debug, rate, rate, 0) }()
			first := make([]float32, rate/4)
			second := make([]float32, rate/4)
			for i := range first {
				first[i] = 0.25
				second[i] = -0.25
			}
			if err := stream.Write(first); err != nil {
				t.Fatal(err)
			}
			if err := stream.Write(second); err != nil {
				t.Fatal(err)
			}
			deadline := time.Now().Add(2 * time.Second)
			for segment.pending() < len(first)+len(second) && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
			}
			if segment.pending() != len(first)+len(second) {
				t.Fatal("pump did not consume source chunks")
			}
			p.mu.Lock()
			p.current = segment
			p.mu.Unlock()
			p.onData(make([]byte, 128), nil, 64)
			// Keep synthesis open. The recorder must retain existing PCM without a
			// deferred end-of-stream source event, for both user stop and output reset.
			if reset {
				if err := p.ResetOutput(context.Background()); err != nil {
					t.Fatal(err)
				}
			} else {
				p.StopDiagnosticCapture()
			}
			source, sourceRate, err := audio.ReadWAVFloat32(filepath.Join(status.Directory, "source-0001-24000hz.wav"))
			if err != nil {
				t.Fatal(err)
			}
			if sourceRate != rate || len(source) != len(first)+len(second) || source[0] < 0.24 || source[len(first)] > -0.24 {
				t.Fatalf("source prefix missing or chunks not contiguous: rate=%d samples=%d", sourceRate, len(source))
			}
			rendered, _, err := audio.ReadWAVFloat32(filepath.Join(status.Directory, "device-output.wav"))
			if err != nil || len(rendered) != 64 {
				t.Fatalf("callback PCM missing: samples=%d err=%v", len(rendered), err)
			}
			cancel()
			stream.Close()
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("pump did not stop")
			}
		})
	}
}

func TestDiagnosticCaptureWriterOpenFailuresStopAndCount(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"device-output.wav", "callbacks.jsonl"} {
		if err := os.Mkdir(filepath.Join(root, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	// Pre-existing directories deterministically reject both writer opens without
	// touching devices, permissions outside the temp tree, or platform paths.
	r := &playerDebugRecorder{dir: root, started: time.Now(), events: make(chan debugAudioEvent, 2)}
	r.wg.Add(1)
	go r.run(24000, 1)
	deadline := time.Now().Add(time.Second)
	for r.status().Active && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	r.close()
	if s := r.status(); s.Active || s.LimitReason != "write_error" || s.DroppedEvents < 2 {
		t.Fatalf("failure status: %+v", s)
	}
	encoded, err := os.ReadFile(filepath.Join(root, "summary.json"))
	if err != nil {
		t.Fatal(err)
	}
	var summary map[string]any
	if err := json.Unmarshal(encoded, &summary); err != nil {
		t.Fatal(err)
	}
	if summary["write_errors"].(float64) < 2 || summary["dropped_debug_events"].(float64) < 2 {
		t.Fatalf("summary hid IO failure: %s", encoded)
	}
}
