package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// The probe can reproduce a 291 ms first token on a request it builds itself,
// while B1 measured 4.5 s through the product's own brain seam on the same
// model, the same machine and the same question. Exactly one of those two
// requests is wrong about what it is sending, and no amount of reasoning about
// the Go code settles which.
//
// So: sit between the brain and Ollama and write down what actually goes over
// the wire. runProxy forwards every request untouched and records the chat
// bodies plus the arrival time of each response chunk. It is transparent by
// construction — no buffering, flushed per chunk — and the way to prove that is
// to run this probe through it and confirm the numbers do not move.

type capture struct {
	Seq          int             `json:"seq"`
	At           string          `json:"at"`
	Path         string          `json:"path"`
	Model        string          `json:"model"`
	Body         json.RawMessage `json:"body"`
	BodyBytes    int             `json:"body_bytes"`
	Options      json.RawMessage `json:"options,omitempty"`
	Think        json.RawMessage `json:"think,omitempty"`
	MessageRoles []string        `json:"message_roles,omitempty"`
	SystemChars  int             `json:"system_chars"`
	ToolCount    int             `json:"tool_count"`
	ToolsChars   int             `json:"tools_chars"`
	KeepAlive    json.RawMessage `json:"keep_alive,omitempty"`

	HeadersMS      float64 `json:"headers_ms"`
	FirstChunkMS   float64 `json:"first_chunk_ms"`
	FirstContentMS float64 `json:"first_content_ms,omitempty"`
	DoneMS         float64 `json:"done_ms"`
	Chunks         int     `json:"chunks"`

	LoadDurationMS       float64 `json:"load_duration_ms"`
	PromptEvalCount      int     `json:"prompt_eval_count"`
	PromptEvalDurationMS float64 `json:"prompt_eval_duration_ms"`
	EvalCount            int     `json:"eval_count"`
	EvalDurationMS       float64 `json:"eval_duration_ms"`
	TotalDurationMS      float64 `json:"total_duration_ms"`
}

type recorder struct {
	mu  sync.Mutex
	f   *os.File
	seq int
}

func (r *recorder) write(c capture) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seq++
	c.Seq = r.seq
	blob, err := json.Marshal(c)
	if err != nil {
		return
	}
	fmt.Fprintf(r.f, "%s\n", blob)
	_ = r.f.Sync()
	fmt.Fprintf(os.Stderr, "  [proxy] #%d %s model=%s prompt_tok=%d first_content=%.0fms prefill=%.0fms load=%.0fms tools=%d sys=%dch\n",
		c.Seq, c.Path, c.Model, c.PromptEvalCount, c.FirstContentMS, c.PromptEvalDurationMS, c.LoadDurationMS, c.ToolCount, c.SystemChars)
}

func runProxy(listen, upstream, outPath string) error {
	f, err := os.Create(outPath)
	if err != nil {
		return err
	}
	defer f.Close()
	rec := &recorder{f: f}

	target := strings.TrimSuffix(upstream, "/")
	if !strings.HasPrefix(target, "http") {
		target = "http://" + target
	}

	// No response timeout: a slow turn is the phenomenon, not a failure.
	client := &http.Client{Transport: &http.Transport{
		DialContext:         (&net.Dialer{Timeout: 5 * time.Second}).DialContext,
		DisableCompression:  true,
		MaxIdleConnsPerHost: 8,
	}}

	h := func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = r.Body.Close()

		out, err := http.NewRequestWithContext(r.Context(), r.Method, target+r.URL.RequestURI(), bytes.NewReader(body))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		out.Header = r.Header.Clone()
		out.Header.Del("Accept-Encoding")

		start := time.Now()
		resp, err := client.Do(out)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()

		recording := r.URL.Path == "/api/chat" || r.URL.Path == "/api/generate"
		c := capture{At: start.Format(time.RFC3339Nano), Path: r.URL.Path, BodyBytes: len(body)}
		if recording {
			c.Body = json.RawMessage(body)
			describeBody(body, &c)
			c.HeadersMS = msSince(start)
		}

		for k, vs := range resp.Header {
			for _, v := range vs {
				w.Header().Add(k, v)
			}
		}
		w.WriteHeader(resp.StatusCode)
		flusher, _ := w.(http.Flusher)

		// Line-oriented so a chunk boundary is a JSON object boundary, and
		// flushed immediately so the proxy adds no buffering of its own.
		br := bufio.NewReaderSize(resp.Body, 64*1024)
		for {
			line, rerr := br.ReadBytes('\n')
			if len(line) > 0 {
				now := time.Now()
				if _, werr := w.Write(line); werr != nil {
					break
				}
				if flusher != nil {
					flusher.Flush()
				}
				if recording {
					c.Chunks++
					if c.FirstChunkMS == 0 {
						c.FirstChunkMS = float64(now.Sub(start).Microseconds()) / 1000
					}
					noteChunk(line, now, start, &c)
				}
			}
			if rerr != nil {
				break
			}
		}
		if recording {
			c.DoneMS = msSince(start)
			rec.write(c)
		}
	}

	srv := &http.Server{Addr: listen, Handler: http.HandlerFunc(h)}
	fmt.Fprintf(os.Stderr, "ttft_probe proxy: %s -> %s, recording to %s\n", listen, target, outPath)
	fmt.Fprintf(os.Stderr, "point the client at it with OLLAMA_HOST=%s\n", listen)
	return srv.ListenAndServe()
}

func describeBody(body []byte, c *capture) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return
	}
	if v, ok := raw["model"]; ok {
		_ = json.Unmarshal(v, &c.Model)
	}
	c.Options = raw["options"]
	c.Think = raw["think"]
	c.KeepAlive = raw["keep_alive"]
	if v, ok := raw["tools"]; ok {
		c.ToolsChars = len(v)
		var tools []json.RawMessage
		if json.Unmarshal(v, &tools) == nil {
			c.ToolCount = len(tools)
		}
	}
	if v, ok := raw["messages"]; ok {
		var msgs []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		}
		if json.Unmarshal(v, &msgs) == nil {
			for _, m := range msgs {
				c.MessageRoles = append(c.MessageRoles, fmt.Sprintf("%s:%d", m.Role, len(m.Content)))
				if m.Role == "system" {
					c.SystemChars += len(m.Content)
				}
			}
		}
	}
}

func noteChunk(line []byte, now, start time.Time, c *capture) {
	var r struct {
		Message struct {
			Content  string `json:"content"`
			Thinking string `json:"thinking"`
		} `json:"message"`
		Response           string  `json:"response"`
		Done               bool    `json:"done"`
		LoadDuration       int64   `json:"load_duration"`
		PromptEvalCount    int     `json:"prompt_eval_count"`
		PromptEvalDuration int64   `json:"prompt_eval_duration"`
		EvalCount          int     `json:"eval_count"`
		EvalDuration       int64   `json:"eval_duration"`
		TotalDuration      int64   `json:"total_duration"`
		_                  float64 `json:"-"`
	}
	if json.Unmarshal(bytes.TrimSpace(line), &r) != nil {
		return
	}
	if c.FirstContentMS == 0 && (r.Message.Content != "" || r.Response != "") {
		c.FirstContentMS = float64(now.Sub(start).Microseconds()) / 1000
	}
	if r.Done {
		c.LoadDurationMS = float64(r.LoadDuration) / 1e6
		c.PromptEvalCount = r.PromptEvalCount
		c.PromptEvalDurationMS = float64(r.PromptEvalDuration) / 1e6
		c.EvalCount = r.EvalCount
		c.EvalDurationMS = float64(r.EvalDuration) / 1e6
		c.TotalDurationMS = float64(r.TotalDuration) / 1e6
	}
}
