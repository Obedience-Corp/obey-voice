package main

import (
	"bufio"
	"os"
	"regexp"
	"strconv"
)

// How much of the prompt Ollama actually re-read is the whole question, and the
// HTTP API cannot answer it: prompt_eval_count reports the entire prompt
// whether the server evaluated it or skipped it from cache, so dividing it by
// prompt_eval_duration yields a rate that can exceed any real throughput. The
// first version of this probe published 59,494 tok/s that way and called it
// prefill.
//
// The server log does answer it, in llama.cpp's own words:
//
//	slot operator():    id 0 | task 15 | new prompt, n_ctx_slot = 8192, n_keep = 4, task.n_tokens = 1194
//	slot operator():    id 0 | task 15 | restored context checkpoint (pos_min = 1065, ..., n_past = 1066, ...)
//	slot print_timing:  id 0 | task 15 | prompt eval time = 702.78 ms / 128 tokens (5.49 ms per token, 182.13 tokens per second)
//
// The prompt was 1194 tokens; the server restored a checkpoint at 1066 and read
// 128. That timing line counts only what was actually read, so it is both the
// true throughput and — against task.n_tokens — the true reuse.
//
// This is what separates the two explanations of the same wall-clock win.
// "Smaller batches read the prompt faster" predicts the rate on that line
// changing with num_batch. "Smaller batches let the server skip more of the
// prompt" predicts the rate staying put while the token count falls. The log
// says which.

var (
	reNewPrompt  = regexp.MustCompile(`task (\d+) \| new prompt, .*task\.n_tokens = (\d+)`)
	rePromptEval = regexp.MustCompile(`task (\d+) \| prompt eval time =\s+([0-9.]+) ms /\s+(\d+) tokens`)
	reRestored   = regexp.MustCompile(`task (\d+) \| restored context checkpoint .*n_past = (\d+)`)
)

// reuseStat is one request's accounting.
type reuseStat struct {
	Task       int     `json:"task"`
	Tokens     int     `json:"prompt_tokens"`
	Reevaled   int     `json:"reevaluated_tokens"`
	Restored   int     `json:"restored_from_checkpoint"`
	EvalMS     float64 `json:"prompt_eval_ms"`
	TokPerSec  float64 `json:"true_tok_per_s"`
	CachedPct  float64 `json:"cached_pct"`
	NoEvalLine bool    `json:"fully_cached"`
}

// caseReuse is what a case's slice of server log adds up to.
type caseReuse struct {
	Samples        int     `json:"samples"`
	PromptTokens   int     `json:"prompt_tokens_median"`
	ReevaluatedTok int     `json:"reevaluated_tokens_median"`
	CachedSharePct float64 `json:"cached_share_pct"`
	TrueTokPerSec  float64 `json:"true_prefill_tok_per_s"`
	Note           string  `json:"note"`
}

// logOffset is where the server log ends right now, so the next case reads only
// its own lines.
func logOffset(path string) int64 {
	fi, err := os.Stat(path)
	if err != nil {
		return -1
	}
	return fi.Size()
}

// readReuse parses the log written since `from`, one record per request.
func readReuse(path string, from int64) ([]reuseStat, bool) {
	if path == "" || from < 0 {
		return nil, false
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, false
	}
	defer f.Close()
	if _, err := f.Seek(from, 0); err != nil {
		return nil, false
	}

	byTask := map[int]*reuseStat{}
	var order []int
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 256*1024), 8*1024*1024)
	get := func(task int) *reuseStat {
		if s, ok := byTask[task]; ok {
			return s
		}
		s := &reuseStat{Task: task, NoEvalLine: true}
		byTask[task] = s
		order = append(order, task)
		return s
	}
	for sc.Scan() {
		line := sc.Text()
		if m := reNewPrompt.FindStringSubmatch(line); m != nil {
			task, _ := strconv.Atoi(m[1])
			n, _ := strconv.Atoi(m[2])
			get(task).Tokens = n
			continue
		}
		if m := reRestored.FindStringSubmatch(line); m != nil {
			task, _ := strconv.Atoi(m[1])
			n, _ := strconv.Atoi(m[2])
			get(task).Restored = n
			continue
		}
		if m := rePromptEval.FindStringSubmatch(line); m != nil {
			task, _ := strconv.Atoi(m[1])
			ms, _ := strconv.ParseFloat(m[2], 64)
			n, _ := strconv.Atoi(m[3])
			s := get(task)
			s.EvalMS, s.Reevaled, s.NoEvalLine = ms, n, false
			if ms > 0 {
				s.TokPerSec = float64(n) / (ms / 1000)
			}
		}
	}

	var out []reuseStat
	for _, task := range order {
		s := byTask[task]
		if s.Tokens == 0 {
			continue // a request whose "new prompt" line fell outside this slice
		}
		if s.Tokens > 0 {
			s.CachedPct = 100 * float64(s.Tokens-s.Reevaled) / float64(s.Tokens)
		}
		out = append(out, *s)
	}
	return out, len(out) > 0
}

// summarizeReuse folds a case's requests together, dropping the discard sample:
// it is the one that primes whatever the case is about to measure, so counting
// it would understate reuse for every case that depends on a primed cache.
func summarizeReuse(stats []reuseStat) *caseReuse {
	if len(stats) <= 1 {
		return nil
	}
	stats = stats[1:]
	var toks, reeval, rates []float64
	for _, s := range stats {
		toks = append(toks, float64(s.Tokens))
		reeval = append(reeval, float64(s.Reevaled))
		if s.TokPerSec > 0 {
			rates = append(rates, s.TokPerSec)
		}
	}
	cr := &caseReuse{
		Samples:        len(stats),
		PromptTokens:   int(pct(toks, 50)),
		ReevaluatedTok: int(pct(reeval, 50)),
		TrueTokPerSec:  pct(rates, 50),
		Note: "reevaluated_tokens and true_prefill_tok_per_s come from llama.cpp's own " +
			"`prompt eval time = X ms / N tokens` line, which counts only what the server read. " +
			"cached_share_pct is the rest of the prompt, restored from a context checkpoint.",
	}
	if cr.PromptTokens > 0 {
		cr.CachedSharePct = 100 * float64(cr.PromptTokens-cr.ReevaluatedTok) / float64(cr.PromptTokens)
	}
	return cr
}
