package netapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"
)

// An offline recognizer cannot answer until the client closes the ingress.
// Deliberately emits no partials and never calls RunTurn/RunTurnTextMode.
func (r *voiceRunner) RunTranscription(ctx context.Context) (string, error) {
	if err := r.drainIngress(ctx); err != nil {
		return "", err
	}
	return "hello from offline recognition", nil
}

func TestTranscriptionWaitsForVoiceEndAndReturnsOnlyToOwner(t *testing.T) {
	runner := &voiceRunner{}
	server, _, addr, creds := startVoiceServer(t, runner)
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	owner := dialStream(t, ctx, addr, creds)
	observer := dialStream(t, ctx, addr, creds)
	defer owner.CloseNow()
	defer observer.CloseNow()
	seen := readFrames(t, ctx, observer)
	writeControl(t, ctx, owner, controlMessage{Type: "transcribe_start", ID: "ask-1"})
	eventually(t, "mic claimed", func() bool { return micClaimed(server.hub) })
	writeControl(t, ctx, owner, controlMessage{Type: "audio_input", Data: base64.StdEncoding.EncodeToString(make([]byte, 3200)), SampleRate: 16000})
	eventually(t, "audio consumed", func() bool { return runner.samplesConsumed() == 1600 })
	writeControl(t, ctx, owner, controlMessage{Type: "voice_end"})
	_, data, err := owner.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]string
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	if result["type"] != "transcription_result" || result["id"] != "ask-1" || result["text"] != "hello from offline recognition" || result["error"] != "" {
		t.Fatalf("result = %s", data)
	}
	eventually(t, "claim released", func() bool { return !micClaimed(server.hub) })
	if got := seen.await("transcription_result", 50*time.Millisecond); len(got) != 0 {
		t.Fatalf("private result was broadcast: %v", got)
	}
	runner.mu.Lock()
	defer runner.mu.Unlock()
	if runner.voices != 0 || len(runner.texts) != 0 || runner.finals != 1 {
		t.Fatalf("voice turns=%d, text turns=%d, EOFs=%d", runner.voices, len(runner.texts), runner.finals)
	}
}
