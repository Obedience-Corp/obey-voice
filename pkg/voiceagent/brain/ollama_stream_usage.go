package brain

import "github.com/ollama/ollama/api"

// streamRoundUsage accounts for generated tokens and publishes progress while
// the response is arriving, including rounds that end with a partial response.
type streamRoundUsage struct {
	prefillTokens, genTokens int
	doneReason               string
	complete                 bool
}

func (u *streamRoundUsage) receiver(o *OllamaBrain, limit *responseBudget, promptEstimate int, output *streamRoundOutput) api.ChatResponseFunc {
	generatedBytes, lastBudget := 0, promptEstimate
	return func(resp api.ChatResponse) error {
		if resp.Done {
			u.prefillTokens, u.genTokens = resp.Metrics.PromptEvalCount, resp.Metrics.EvalCount
			u.doneReason = resp.DoneReason
		}
		if err := limit.observe(resp); err != nil {
			return err
		}
		generatedBytes += len(resp.Message.Content) + len(resp.Message.Thinking)
		estimate := promptEstimate + (generatedBytes+3)/4
		// Bound telemetry volume while keeping long replies visibly live.
		if estimate-lastBudget >= 64 {
			o.emitContinuityBudget(estimate)
			lastBudget = estimate
		}
		u.complete = u.complete || resp.Done
		return output.receive(resp.Message)
	}
}

func (u *streamRoundUsage) report(opts StreamOptions) {
	if opts.OnUsage != nil {
		opts.OnUsage(u.prefillTokens, u.genTokens)
	}
	if opts.OnWorkerUsage != nil {
		worker := WorkerUsage{}
		if u.prefillTokens > 0 {
			in := u.prefillTokens
			worker.InputTokens = &in
		}
		if u.genTokens > 0 {
			out := u.genTokens
			worker.OutputTokens = &out
		}
		opts.OnWorkerUsage(worker)
	}
}
