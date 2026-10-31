package brain

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/wav"
	"github.com/ollama/ollama/api"
)

const nativeContextCeiling = 32768

// nativeMessagesBudgetError uses the audited profile only for its exact checked
// runtime/model identity. Other identities retain an explicitly labeled estimate:
// UTF-8/tool JSON bytes, 128 units/message and 100 audio frames/second plus 64.
// Neither path automatically trims evidence or expands the 32K client ceiling.
func (o *OllamaBrain) nativeMessagesBudgetError(messages []api.Message, tools api.Tools) error {
	if o.nativeContextIdentity.pinnedLFM3() {
		allowance, err := pinnedLFM3ContextBound(messages, tools)
		if err != nil {
			return err
		}
		return o.nativeAdmissionError(allowance)
	}
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
	return o.nativeAdmissionError(allowance)
}

func (o *OllamaBrain) nativeAdmissionError(allowance int64) error {
	budget := o.effectiveNumCtx()
	if budget <= 0 {
		budget = nativeContextCeiling
	}
	if allowance > int64(budget)*9/10 {
		return fmt.Errorf("%w (%d conservative native admission units, num_ctx=%d); start a new context or explicitly provide a transcript/summary", ErrPromptBudget, allowance, budget)
	}
	return nil
}
