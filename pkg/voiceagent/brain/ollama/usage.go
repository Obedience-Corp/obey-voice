package ollama

import "github.com/ollama/ollama/api"

// Usage records the terminal metrics Ollama supplied for one request.
type Usage struct {
	InputTokens, OutputTokens int
	DoneReason                string
	Complete                  bool
}

// Receiver observes the request budget before forwarding each model delta.
func (r *Round) Receiver(promptEstimate int, observe func(api.ChatResponse) error, progress func(int)) api.ChatResponseFunc {
	generatedBytes, lastBudget := 0, promptEstimate
	return func(resp api.ChatResponse) error {
		if resp.Done {
			r.Usage.InputTokens, r.Usage.OutputTokens = resp.Metrics.PromptEvalCount, resp.Metrics.EvalCount
			r.Usage.DoneReason = resp.DoneReason
		}
		// A delta ending the round keeps its calls even when a limit or a text
		// filter drops the rest of it.
		ends := r.endsRound(resp.Message)
		if ends {
			r.ToolCalls = append(r.ToolCalls, resp.Message.ToolCalls...)
		}
		if err := observe(resp); err != nil {
			return err
		}
		generatedBytes += len(resp.Message.Content) + len(resp.Message.Thinking)
		estimate := promptEstimate + (generatedBytes+3)/4
		// Bound telemetry volume while keeping long replies visibly live.
		if estimate-lastBudget >= 64 {
			progress(estimate)
			lastBudget = estimate
		}
		r.Usage.Complete = r.Usage.Complete || resp.Done
		msg := resp.Message
		if ends {
			msg.ToolCalls = nil
		}
		err := r.receive(msg)
		if ends {
			r.ended = true
		}
		return err
	}
}
