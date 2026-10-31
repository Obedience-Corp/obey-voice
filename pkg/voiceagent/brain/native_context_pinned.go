package brain

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/wav"
	"github.com/ollama/ollama/api"
)

// NativeContextIdentity comes from the selected candidate's checked metadata.
// Unknown versions/digests retain the general estimate, never this pinned bound.
type NativeContextIdentity struct{ RuntimeVersion, Digest string }

func (i NativeContextIdentity) pinnedLFM3() bool {
	return i.RuntimeVersion == "0.40.12" && i.Digest == "19ff9eb6e621de5643e27d7a3d64e3fe62f9cd22d1471c9a34c01fb69b8e4f74"
}

// The pinned byte-level BPE has no normalizer or prefix/suffix expansion;
// merges cannot increase its initial one-token-per-UTF8-byte sequence. Count
// every possible renderer wrapper literally, even mutually exclusive branches.
// Audio placeholders are replaced by exact encoder outputs before the runner's
// hard context check. See docs/native-audio/context-contract.md for provenance.
func pinnedLFM3ContextBound(messages []api.Message, tools api.Tools) (int64, error) {
	total := int64(len("<|startoftext|><|im_start|>assistant\n<think></think>"))
	for _, message := range messages {
		total += int64(len(message.Role) + len(message.Content) + len(message.Thinking))
		total += int64(len("<|im_start|>\n<|im_end|>\n<think></think><tool_response>\n\n</tool_response>"))
		for _, call := range message.ToolCalls {
			total += int64(len("\n\n<tool_call>\n<function=>\n</function>\n</tool_call>") + len(call.Function.Name))
			for name, value := range call.Function.Arguments.All() {
				encoded, err := json.Marshal(value)
				if err != nil {
					return 0, fmt.Errorf("%w: native tool argument: %v", ErrAudioProjection, err)
				}
				// JSON string escaping/quotes only overcount the renderer's raw string.
				total += int64(len(name) + len(encoded) + len("<parameter=>\n\n</parameter>\n"))
			}
		}
		for _, recording := range message.Images {
			format, err := wav.ReadWAVFormat(bytes.NewReader(recording), int64(len(recording)))
			if err != nil || format.SampleRate != 16000 || format.Channels != 1 || format.BitsPerSample != 16 || format.Frames() <= 0 {
				return 0, fmt.Errorf("%w: invalid native context recording", ErrAudioProjection)
			}
			frames := int64(1)
			if format.Frames() >= 400 {
				frames += (format.Frames() - 400 + 159) / 160
			}
			// Two optional vision delimiters and a separator cover both lead branches.
			total += (frames+7)/8 + 3
		}
	}
	if len(tools) > 0 {
		encoded, err := json.Marshal(tools)
		if err != nil {
			return 0, fmt.Errorf("%w: native tools: %v", ErrAudioProjection, err)
		}
		total += int64(len(encoded) + len(tools) + len("\n\n<tools>\n\n</tools>"))
	}
	return total, nil
}
