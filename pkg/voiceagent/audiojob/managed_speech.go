package audiojob

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"mime"
	"net/http"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
)

func (p *ManagedRuntime) Synthesize(ctx context.Context, text string) (*audio.PCMStream, error) {
	if len(text) == 0 || len(text) > 1024 {
		return nil, fmt.Errorf("managed narration requires a script of 1–1024 UTF-8 bytes")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	stream := audio.NewPCMStream(ctx)
	_ = stream.SetSampleRate(24000)
	stream.SetIncremental(true)
	go func() { stream.CloseWithError(p.speak(ctx, stream, text)) }()
	return stream, nil
}

func (p *ManagedRuntime) speak(ctx context.Context, stream *audio.PCMStream, text string) error {
	payload, err := json.Marshal(map[string]any{
		"input": text, "voice": p.voice, "lang": "en", "response_format": "pcm",
		"stream": true, "seed": 42, "n_predict": 8192, "repeat_penalty": 1.05,
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint+"/tts", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+p.token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := p.client.Do(req)
	if err != nil {
		return fmt.Errorf("managed narration: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("managed narration returned HTTP %d", resp.StatusCode)
	}
	media, params, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if err != nil || media != "audio/pcm" || params["rate"] != "24000" || params["encoding"] != "float" || params["bits"] != "32" {
		return fmt.Errorf("managed narration returned unexpected PCM format")
	}
	return readFloatPCM(resp.Body, stream)
}

// llama.cpp's raw PCM is float32, unlike Ollama's PCM16 speech endpoint. Keep
// decoding explicit so the two successful HTTP responses cannot be confused.
func readFloatPCM(reader io.Reader, stream *audio.PCMStream) error {
	buf := make([]byte, 4096)
	carry, emptyReads := 0, 0
	for {
		n, err := reader.Read(buf[carry:])
		if n == 0 && err == nil {
			emptyReads++
			if emptyReads >= 100 {
				return io.ErrNoProgress
			}
			continue
		}
		emptyReads = 0
		total := n + carry
		complete := total - total%4
		if complete > 0 {
			samples := make([]float32, complete/4)
			for i := range samples {
				samples[i] = math.Float32frombits(binary.LittleEndian.Uint32(buf[i*4:]))
				if math.IsInf(float64(samples[i]), 0) || math.IsNaN(float64(samples[i])) {
					return fmt.Errorf("managed narration returned non-finite PCM")
				}
			}
			if writeErr := stream.Write(samples); writeErr != nil {
				return writeErr
			}
		}
		carry = copy(buf, buf[complete:total])
		if err == io.EOF {
			if carry != 0 {
				return fmt.Errorf("managed narration ended within a PCM sample")
			}
			return nil
		}
		if err != nil {
			return err
		}
	}
}
