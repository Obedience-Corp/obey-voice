package brain

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/wav"
	"github.com/ollama/ollama/api"
)

const nativeContextCeiling = 32768

// nativeMessagesBudgetError is conservative admission, not tokenizer accounting.
// Text/tool UTF-8 bytes receive one unit each (rather than Standard's /4).
// Audio receives one unit per 10ms frame plus64 per recording, deliberately
// above observed LFM3 prefix/stride counts. This estimate cannot establish that
// a runtime honors num_ctx; the reproducible runtime discrepancy remains open.
// Native evidence is never automatically trimmed to satisfy this allowance.
func (o *OllamaBrain) nativeMessagesBudgetError(messages []api.Message, tools api.Tools) error {
	allowance := int64(0)
	for _, message := range messages {
		allowance += int64(len(message.Content)) + 128
		if len(message.ToolCalls) > 0 {
			data, _ := json.Marshal(message.ToolCalls)
			allowance += int64(len(data))
		}
		for _, image := range message.Images {
			format, err := wav.ReadWAVFormat(bytes.NewReader(image), int64(len(image)))
			if err != nil || format.SampleRate != 16000 || format.Channels != 1 || format.BitsPerSample != 16 {
				return fmt.Errorf("%w: invalid native context recording", ErrAudioProjection)
			}
			allowance += (format.Frames()+159)/160 + 64
		}
	}
	if len(tools) > 0 {
		data, _ := json.Marshal(tools)
		allowance += int64(len(data))
	}
	budget := o.effectiveNumCtx()
	if budget <= 0 {
		budget = nativeContextCeiling
	}
	if allowance > int64(budget)*9/10 {
		return fmt.Errorf("%w (%d conservative native admission units, num_ctx=%d); start a new context or explicitly provide a transcript/summary", ErrPromptBudget, allowance, budget)
	}
	return nil
}
