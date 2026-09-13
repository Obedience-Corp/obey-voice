package qwen

import (
	"context"
	"errors"
	tts "github.com/Obedience-Corp/obey-voice/pkg/voiceagent/tts"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

// writePlaceTestWorker writes a Stage A script worker: ready, then nothing but
// shutdown handling. Construction is what these cases test.
func writePlaceTestWorker(t *testing.T) string {
	t.Helper()
	script := filepath.Join(t.TempDir(), "qwen3-tts-worker")
	source := `#!/usr/bin/env bash
set -euo pipefail
echo '{"type":"ready","protocol":"qwen3-tts-worker/v1","sample_rate":24000,"pcm_format":"f32le","streaming":false,"presets":["Vivian"]}'
while IFS= read -r line; do
  [[ "$line" == *'"shutdown"'* ]] && exit 0
done
`
	if err := os.WriteFile(script, []byte(source), 0o700); err != nil {
		t.Fatal(err)
	}
	return script
}

func TestNewQwen3TTSPlace(t *testing.T) {
	script := writePlaceTestWorker(t)
	modelDir := t.TempDir()

	for _, tc := range []struct {
		name  string
		place string
		url   string
		want  string
	}{
		{name: "lan without a url fails validation", place: config.TTSPlaceLAN, want: "tts_worker_url"},
		{name: "unset place spawns locally"},
		{name: "explicit local spawns locally", place: config.TTSPlaceLocal},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q, err := NewQwen3TTS(&config.Config{
				QwenTTSBinary:  script,
				QwenTTSModel:   modelDir,
				QwenTTSTimeout: 30,
				TTSPlace:       tc.place,
				TTSWorkerURL:   tc.url,
			})
			if tc.want != "" {
				if err == nil || !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("err=%v want %q", err, tc.want)
				}
				var provErr *tts.ProviderError
				if errors.As(err, &provErr) && provErr.Kind != tts.ProviderErrorUnavailable {
					t.Fatalf("kind=%q want %q", provErr.Kind, tts.ProviderErrorUnavailable)
				}
				return
			}
			if err != nil {
				t.Fatalf("NewQwen3TTS: %v", err)
			}
			defer q.Delete()
			if !q.native {
				t.Fatal("local place must resolve the native worker")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			q.sessionMu.Lock()
			err = q.ensureSpeechSessionLocked(ctx)
			q.sessionMu.Unlock()
			if err != nil {
				t.Fatalf("ensureSpeechSessionLocked: %v", err)
			}
		})
	}
}
