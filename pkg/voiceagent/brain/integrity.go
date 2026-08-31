package brain

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

const (
	continuityPromptMargin   = 32
	continuityMinCompleteGen = 64
	continuitySoftLimitRatio = 80 // percent; matches DECISIONS D3 soft limit
)

// IntegrityInput captures stream completion signals for integrity evaluation.
type IntegrityInput struct {
	DoneReason   string
	PromptTokens int
	GenTokens    int
	NumCtx       int
	Speakable    string
	HasToolCalls bool
}

func evaluateStreamIntegrity(in IntegrityInput) IntegrityResult {
	speakable := strings.TrimSpace(in.Speakable)
	midSentence := !in.HasToolCalls &&
		in.GenTokens < continuityMinCompleteGen &&
		speakable != "" &&
		endsMidSentence(speakable)

	lengthStop := strings.EqualFold(strings.TrimSpace(in.DoneReason), "length")
	promptSaturated := in.NumCtx > 0 && in.PromptTokens >= in.NumCtx-continuityPromptMargin
	truncated := lengthStop || promptSaturated || midSentence

	return IntegrityResult{
		Truncated:    truncated,
		MidSentence:  midSentence,
		SpokenSoFar:  speakable,
		PromptTokens: in.PromptTokens,
		GenTokens:    in.GenTokens,
		DoneReason:   strings.TrimSpace(in.DoneReason),
	}
}

func endsMidSentence(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	last, _ := utf8.DecodeLastRuneInString(s)
	switch last {
	case '.', '?', '!', '…':
		return false
	}
	return true
}

var (
	continuitySinkMu sync.Mutex
	continuitySink   func(events.Event)
)

// SetContinuitySink routes continuity observability events onto the app bus.
func SetContinuitySink(fn func(events.Event)) {
	continuitySinkMu.Lock()
	continuitySink = fn
	continuitySinkMu.Unlock()
}

func emitContinuity(e events.Event) {
	continuitySinkMu.Lock()
	sink := continuitySink
	continuitySinkMu.Unlock()
	if sink != nil {
		sink(e)
	}
}

func (o *OllamaBrain) continuityIntegrityEnabled() bool {
	if o.cfg == nil {
		return true
	}
	return o.cfg.ContinuityIntegrityEnabled
}

func (o *OllamaBrain) maybeEmitContinuityBudget(skillCtx string) {
	if !o.continuityIntegrityEnabled() || o.cfg == nil {
		return
	}
	numCtx := o.cfg.OllamaNumCtx
	if numCtx <= 0 {
		return
	}
	est := o.estimateTokens(skillCtx)
	soft := numCtx * continuitySoftLimitRatio / 100
	hard := numCtx * 9 / 10
	action := "ok"
	switch {
	case est > hard:
		action = "over_hard"
	case est > soft:
		action = "over_soft"
	}
	evt := events.ContinuityBudget{
		EstTokens: est,
		SoftLimit: soft,
		HardLimit: hard,
		NumCtx:    numCtx,
		Action:    action,
	}
	fmt.Fprintf(os.Stderr,
		"obey-voice: continuity.budget est=%d soft=%d hard=%d num_ctx=%d action=%s\n",
		evt.EstTokens, evt.SoftLimit, evt.HardLimit, evt.NumCtx, evt.Action)
	emitContinuity(evt)
}

func (o *OllamaBrain) emitContinuityTruncated(r IntegrityResult) {
	evt := events.ContinuityTruncated{
		DoneReason:   r.DoneReason,
		PromptTokens: r.PromptTokens,
		GenTokens:    r.GenTokens,
		MidSentence:  r.MidSentence,
	}
	fmt.Fprintf(os.Stderr,
		"obey-voice: continuity.truncated done_reason=%q prompt=%d gen=%d mid_sentence=%t\n",
		evt.DoneReason, evt.PromptTokens, evt.GenTokens, evt.MidSentence)
	emitContinuity(evt)
}
