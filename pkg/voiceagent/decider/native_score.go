package decider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"sort"
	"strings"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/decision"
)

// nativeScorer exposes only the application's typed decision contract. The
// llama worker is private; callers cannot select files, models or endpoints.
type nativeScorer struct {
	client *http.Client
	base   string
	model  Model
}

func (n *nativeScorer) post(ctx context.Context, path string, input, output any) error {
	raw, err := json.Marshal(input)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, n.base+path, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := n.client.Do(req)
	if err != nil {
		return fmt.Errorf("native decision request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("native decision HTTP %d", resp.StatusCode)
	}
	const limit = 64 << 20
	raw, err = io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return err
	}
	if len(raw) > limit {
		return fmt.Errorf("native decision response exceeds limit")
	}
	if err = json.Unmarshal(raw, output); err != nil {
		return fmt.Errorf("decode native decision: %w", err)
	}
	return nil
}

func (n *nativeScorer) evaluate(ctx context.Context, state json.RawMessage, questions map[string]decision.Question) (decision.Response, error) {
	result := decision.Response{Model: n.model.Name, Answers: map[string]decision.Answer{}}
	if len(questions) == 0 || len(questions) > 8 {
		return result, fmt.Errorf("expected 1..8 decision questions")
	}
	for _, q := range questions {
		if q.Type != "choice" && q.Type != "noul" {
			return result, fmt.Errorf("unsupported application decision type %q", q.Type)
		}
		if q.Instructions == "" {
			return result, fmt.Errorf("missing decision instructions")
		}
		if q.Type == "choice" && (len(q.Criteria) < 2 || len(q.Criteria) > 10) {
			return result, fmt.Errorf("application choices require 2..10 options")
		}
	}
	if n.model.Engine == "liquid" {
		err := n.post(ctx, "/v1/systemone", map[string]any{"state": state, "questions": questions}, &result)
		if err != nil {
			return result, err
		}
		// Liquid's pinned API defines choice confidence as p(winner), unlike
		// llama.cpp's normalized-over-uniform confidence. Keep policy gates intact.
		for key, q := range questions {
			a, ok := result.Answers[key]
			if !ok {
				return result, fmt.Errorf("missing native answer %s", key)
			}
			if q.Type == "choice" {
				p, ok := a.Probabilities[a.Choice]
				if !ok || math.IsNaN(p) || p < 0 || p > 1 {
					return result, fmt.Errorf("invalid native choice")
				}
				a.Confidence = &p
				result.Answers[key] = a
			}
		}
		return result, nil
	}
	rendered, err := renderMapikaState(state)
	if err != nil {
		return result, err
	}
	// Match the reference's separately tokenized context and question, including
	// its 32768-token state cap. Never silently truncate the question.
	prefix, err := n.tokenize(ctx, "Context:\n"+rendered)
	if err != nil {
		return result, err
	}
	if len(prefix) > 32768 {
		prefix = prefix[:32768]
	}
	keys := make([]string, 0, len(questions))
	for k := range questions {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, key := range keys {
		q := questions[key]
		names := []string{"false", "true"}
		options := []string{"no", "yes"}
		if q.Type == "choice" {
			names = make([]string, 0, len(q.Criteria))
			for k := range q.Criteria {
				names = append(names, k)
			}
			sort.Strings(names)
			options = make([]string, len(names))
			for i, k := range names {
				options[i] = k
				if q.Criteria[k] != "" {
					options[i] += ": " + q.Criteria[k]
				}
			}
		} else {
			for i, k := range names {
				if q.Criteria[k] != "" {
					options[i] += ": " + q.Criteria[k]
				}
			}
		}
		var piece strings.Builder
		fmt.Fprintf(&piece, "\n\nQuestion: %s\nOptions:", q.Instructions)
		for i, o := range options {
			fmt.Fprintf(&piece, "\n(%c) %s", 'A'+i, o)
		}
		piece.WriteString("\nAnswer: (")
		suffix, err := n.tokenize(ctx, piece.String())
		if err != nil {
			return result, err
		}
		ids := append(append([]int(nil), prefix...), suffix...)
		if len(ids) > 40959 {
			return result, fmt.Errorf("decision prompt exceeds native context")
		}
		labels := make([]int, len(options))
		for i := range labels {
			tok, err := n.tokenize(ctx, string(rune('A'+i)))
			if err != nil {
				return result, err
			}
			if len(tok) != 1 {
				return result, fmt.Errorf("decision label is not one token")
			}
			labels[i] = tok[0]
		}
		temp := n.model.Temperature
		if t := n.model.TemperatureByType[q.Type]; t > 0 {
			temp = t
		}
		probs, err := n.scoreLabels(ctx, ids, labels, temp)
		if err != nil {
			return result, err
		}
		a := decision.Answer{}
		if q.Type == "noul" {
			v := roundProbability(probs[1])
			a.Noul = &v
		} else {
			best := 0
			a.Probabilities = map[string]float64{}
			for i, p := range probs {
				a.Probabilities[names[i]] = roundProbability(p)
				if p > probs[best] {
					best = i
				}
			}
			a.Choice = names[best]
			v := roundProbability(math.Max(0, (float64(len(probs))*probs[best]-1)/float64(len(probs)-1)))
			a.Confidence = &v
		}
		result.Answers[key] = a
	}
	return result, nil
}
func roundProbability(v float64) float64 { return math.RoundToEven(v*10000) / 10000 }
func (n *nativeScorer) tokenize(ctx context.Context, text string) ([]int, error) {
	var out struct {
		Tokens []int `json:"tokens"`
	}
	err := n.post(ctx, "/tokenize", map[string]any{"content": text, "add_special": false, "parse_special": true}, &out)
	if err == nil && len(out.Tokens) == 0 && text != "" {
		err = fmt.Errorf("native tokenizer returned no tokens")
	}
	return out.Tokens, err
}
func (n *nativeScorer) scoreLabels(ctx context.Context, ids, labels []int, temp float64) ([]float64, error) {
	if temp <= 0 || math.IsInf(temp, 0) || math.IsNaN(temp) {
		return nil, fmt.Errorf("invalid decision temperature")
	}
	// Read pre-sampling log probabilities, not generated text or filtered sampler
	// probabilities. Their shared log-normalizer cancels in the label softmax.
	// Usually every trained label is in the first 256. Retry with the full pinned
	// vocabulary if needed, so a low-ranked option is never assigned a fake zero.
	for _, count := range []int{256, 248320} {
		var out struct {
			Probabilities []struct {
				Top []struct {
					ID      int      `json:"id"`
					LogProb *float64 `json:"logprob"`
				} `json:"top_logprobs"`
			} `json:"completion_probabilities"`
			Truncated bool `json:"truncated"`
		}
		err := n.post(ctx, "/completion", map[string]any{"prompt": ids, "n_predict": 1, "temperature": 0, "n_probs": count, "post_sampling_probs": false, "cache_prompt": false, "seed": 0}, &out)
		if err != nil {
			return nil, err
		}
		if out.Truncated || len(out.Probabilities) != 1 {
			return nil, fmt.Errorf("invalid native label readout")
		}
		values := map[int]float64{}
		for _, v := range out.Probabilities[0].Top {
			if v.LogProb != nil && !math.IsNaN(*v.LogProb) && !math.IsInf(*v.LogProb, 0) {
				values[v.ID] = *v.LogProb
			}
		}
		scores := make([]float64, len(labels))
		complete := true
		maxScore := math.Inf(-1)
		for i, id := range labels {
			v, ok := values[id]
			if !ok {
				complete = false
				break
			}
			scores[i] = v / temp
			maxScore = math.Max(maxScore, scores[i])
		}
		if !complete {
			continue
		}
		sum := 0.0
		for i, s := range scores {
			scores[i] = math.Exp(s - maxScore)
			sum += scores[i]
		}
		for i := range scores {
			scores[i] /= sum
		}
		return scores, nil
	}
	return nil, fmt.Errorf("native readout omitted decision labels")
}
