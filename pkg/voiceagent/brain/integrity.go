package brain

import (
	"fmt"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/textclean"
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
	s = strings.TrimSpace(textclean.StripStageDirections(s))
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

// SetContinuitySink routes this brain's continuity observability events onto
// its owning pipeline's event bus. Instance-scoped: with several pipelines
// active, each brain's telemetry stays on its own conversation's bus.
func (o *OllamaBrain) SetContinuitySink(fn func(events.Event)) {
	o.continuityMu.Lock()
	o.continuitySink = fn
	o.continuityMu.Unlock()
}

func (o *OllamaBrain) emitContinuity(e events.Event) {
	o.continuityMu.Lock()
	sink := o.continuitySink
	o.continuityMu.Unlock()
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
	numCtx := o.effectiveNumCtx()
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
	o.emitContinuity(evt)
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
	o.emitContinuity(evt)
}
