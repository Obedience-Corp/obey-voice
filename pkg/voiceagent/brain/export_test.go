package brain

import "github.com/Obedience-Corp/obey-voice/pkg/voiceagent/delegation"

// DelegationSummarizer exposes the conductor's summary transform to the
// external tests in this directory.
func (o *OllamaBrain) DelegationSummarizer() delegation.Summarizer { return o.delegationSummarizer() }
