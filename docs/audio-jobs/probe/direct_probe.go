package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"github.com/coder/websocket"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
)

func main() {
	root := os.Getenv("AUDIO_JOB_PROOF_ROOT")
	if root == "" {
		root = "/tmp/wi-43b072-sol"
	}
	hash := func(path string) string {
		f, e := os.Open(path)
		if e != nil {
			panic(e)
		}
		defer f.Close()
		h := sha256.New()
		if _, e = io.Copy(h, f); e != nil {
			panic(e)
		}
		return hex.EncodeToString(h.Sum(nil))
	}
	modelHash := hash(root + "/gguf/model.gguf")
	projectorHash := hash(root + "/gguf/mmproj.gguf")
	if modelHash != "9a773289bd321ed879cd3a76545f6aca506e403851a45fb315420aee45625be2" || projectorHash != "2b6648b1fd48af16aa0877b0592b144d15070783673e038a379fdbc51d40e365" {
		panic("asset digest changed")
	}
	versionBytes, e := exec.Command(root+"/llama-runtime/llama-b11960/llama-server", "--version").CombinedOutput()
	if e != nil || !strings.Contains(string(versionBytes), "build 11960") {
		panic("unqualified runtime version")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	c, _, e := websocket.Dial(ctx, "ws://127.0.0.1:18143/v1/realtime", nil)
	if e != nil {
		panic(e)
	}
	defer func() { _ = c.CloseNow() }()
	c.SetReadLimit(8 << 20)
	read := func() map[string]any {
		_, b, e := c.Read(ctx)
		if e != nil {
			panic(e)
		}
		var x map[string]any
		if e = json.Unmarshal(b, &x); e != nil {
			panic(e)
		}
		return x
	}
	send := func(x any) {
		b, _ := json.Marshal(x)
		if e = c.Write(ctx, websocket.MessageText, b); e != nil {
			panic(e)
		}
	}
	created := read()
	b, _ := json.Marshal(created)
	fmt.Println(string(b))
	instructions := "Repeat all spoken words from the recording exactly as heard, using a female voice. Preserve every word, repetition, pause and the original duration. Do not answer the speakers or summarize. Respond with interleaved text and audio."
	send(map[string]any{"type": "session.update", "session": map[string]any{"type": "realtime", "instructions": instructions, "output_modalities": []string{"text", "audio"}, "max_output_tokens": 4096, "tool_choice": "none"}})
	updated := read()
	fmt.Println(updated)
	session := created["session"].(map[string]any)
	audioInfo := session["audio"].(map[string]any)
	inRate := audioInfo["input"].(map[string]any)["format"].(map[string]any)["rate"].(float64)
	outRate := audioInfo["output"].(map[string]any)["format"].(map[string]any)["rate"].(float64)
	if inRate != 16000 || outRate != 24000 {
		panic("unexpected runtime audio format")
	}

	f, e := os.Open(root + "/source.wav")
	if e != nil {
		panic(e)
	}
	st, _ := f.Stat()
	format, e := audio.ReadWAVFormat(f, st.Size())
	if e != nil {
		panic(e)
	}
	_, _ = f.Seek(format.DataOffset, 0)
	pcm, e := io.ReadAll(io.LimitReader(f, format.DataBytes))
	if e != nil {
		panic(e)
	}
	_ = f.Close()
	for offset := 0; offset < len(pcm); offset += 32000 {
		end := min(offset+32000, len(pcm))
		send(map[string]any{"type": "input_audio_buffer.append", "audio": base64.StdEncoding.EncodeToString(pcm[offset:end])})
	}
	send(map[string]any{"type": "response.create"})
	start := time.Now()
	var output []byte
	var text string
	var events []map[string]any
	for {
		event := read()
		kind, _ := event["type"].(string)
		switch kind {
		case "response.output_audio.delta":
			delta, _ := event["delta"].(string)
			chunk, e := base64.StdEncoding.DecodeString(delta)
			if e != nil {
				panic(e)
			}
			output = append(output, chunk...)
			delete(event, "delta")
			event["pcm_bytes"] = len(chunk)
		case "response.output_text.delta":
			delta, _ := event["delta"].(string)
			text += delta
		case "error":
			fmt.Println(event)
			panic("runtime error")
		}
		events = append(events, event)
		if kind == "response.done" {
			response := event["response"].(map[string]any)
			if response["status"] != "completed" {
				panic("non-completed response")
			}
			break
		}
	}
	if len(output) == 0 || len(output)%2 != 0 {
		panic("empty or odd PCM output")
	}
	samples := make([]float32, len(output)/2)
	for i := range samples {
		samples[i] = float32(int16(binary.LittleEndian.Uint16(output[2*i:]))) / 32768
	}
	if e = audio.WriteWAVFloat32(root+"/direct-output.wav", 24000, samples); e != nil {
		panic(e)
	}
	receipt := map[string]any{"session_created": created, "session_updated": updated, "input_sample_rate": inRate, "output_sample_rate": outRate, "runtime_version": strings.TrimSpace(string(versionBytes)), "runtime_sha256": hash(root + "/llama-runtime/llama-b11960/llama-server"), "model_sha256": modelHash, "projector_sha256": projectorHash, "source_sha256": hash(root + "/source.wav"), "output_sha256": hash(root + "/direct-output.wav"), "transport": "direct_audio_to_audio", "input_samples": format.Frames(), "input_seconds": float64(format.Frames()) / float64(format.SampleRate), "source_origin_samples": 2080000, "output_samples": len(samples), "output_seconds": float64(len(samples)) / 24000, "text": text, "elapsed_seconds": time.Since(start).Seconds(), "instructions": instructions, "events": events, "qualified_conversion": false, "listener_test": false}
	b, _ = json.MarshalIndent(receipt, "", "  ")
	_ = os.WriteFile(root+"/direct-receipt.json", b, 0600)
	fmt.Printf("text=%q samples=%d duration=%.2fs\n", text, len(samples), float64(len(samples))/24000)
}
