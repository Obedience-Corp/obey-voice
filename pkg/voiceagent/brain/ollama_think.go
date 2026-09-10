package brain

import (
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/textclean"
	"github.com/ollama/ollama/api"
)

// restatementSkip reports whether soFar is still a restatement of already —
// text this turn already streamed, usually before a tool call. Local models
// often emit the spoken line, fire a phantom tool, then emit the same line
// again. Forwarding the second copy is how Veronica said one sentence twice.
// soFar is compared as speakable text: think tags in a restated Content
// must not hide that it is the same reply.
func restatementSkip(already, soFar string) bool {
	already = strings.TrimSpace(already)
	soFar = strings.TrimSpace(textclean.StripThinkBlocks(soFar))
	if already == "" || soFar == "" {
		return false
	}
	return soFar == already || strings.HasPrefix(already, soFar)
}

// fallbackEmptyContent is the empty-Content table: recover when there is
// nothing speakable and the turn either used tools or never thought.
func fallbackEmptyContent(cleaned string, hadTools, hadThinking bool) bool {
	return !hasSpeakableContent(cleaned) && (hadTools || !hadThinking)
}

func messageHadThinking(msg api.Message) bool {
	if strings.TrimSpace(msg.Thinking) != "" {
		return true
	}
	var f textclean.ThinkFilter
	_, inner := f.FeedSplit(msg.Content)
	_, held := f.FlushSplit()
	return inner != "" || held != ""
}

// Think levels ollama_think accepts beyond the boolean forms.
const (
	thinkLow    = "low"
	thinkMedium = "medium"
	thinkHigh   = "high"
	thinkMax    = "max"
)

// ThinkLevels are the canonical ollama_think values a picker should offer, in
// increasing order of effort. The parser also accepts off/0/no and on/1/yes as
// aliases of the two booleans; those are understood, not offered.
//
// It is the source of truth for the ollama_think setting: a level the parser
// gains is invisible to Settings until it is listed here.
func ThinkLevels() []string {
	return []string{"auto", "false", "true", thinkLow, thinkMedium, thinkHigh, thinkMax}
}

// ThinkLevelAccepted reports whether ollamaThinkValue understands value. It
// exists so the settings schema can offer only values the parser honors —
// an unknown value silently disables thinking, which would be invisible in a
// picker.
func ThinkLevelAccepted(value string) bool {
	switch strings.TrimSpace(strings.ToLower(value)) {
	case "", "auto", "false", "off", "0", "no", "true", "on", "1", "yes",
		thinkLow, thinkMedium, thinkHigh, thinkMax:
		return true
	default:
		return false
	}
}

// PreferOllamaThink reports whether model is in the qwen3 family that dumps
// untagged chain-of-thought into Content unless the API Think field is on.
func PreferOllamaThink(model string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(model)), "qwen3")
}

// resolveOllamaThink maps config ollama_think onto the API Think field.
// Empty and "auto" leave thinking off (voice must not pay a think pass on
// every turn). PreferOllamaThink is not applied here — forcing Think:true
// for qwen3 made greetings take a minute on CPU. Explicit true/low/… still
// enable it. Keys off raw, not viper.
func resolveOllamaThink(raw, _ string) *api.ThinkValue {
	s := strings.TrimSpace(strings.ToLower(raw))
	if s == "" || s == "auto" {
		return &api.ThinkValue{Value: false}
	}
	return ollamaThinkValue(raw)
}

func (o *OllamaBrain) resolvedThink() *api.ThinkValue {
	raw := ""
	if o.cfg != nil {
		raw = o.cfg.OllamaThink
	}
	v := resolveOllamaThink(raw, o.model)
	s := strings.TrimSpace(strings.ToLower(raw))
	if s == "false" || s == "off" || s == "0" || s == "no" {
		o.maybeWarnThinkOff(v)
	}
	return v
}

var thinkOffOnce sync.Once

func (o *OllamaBrain) maybeWarnThinkOff(think *api.ThinkValue) {
	if !PreferOllamaThink(o.model) || think == nil || think.Value != false {
		return
	}
	thinkOffOnce.Do(func() {
		msg := fmt.Sprintf("thinking model %q with ollama_think=false; untagged CoT can be spoken", o.model)
		fmt.Fprintf(os.Stderr, "obey-voice: %s\n", msg)
		o.emitInfo(events.Info{Message: msg})
	})
}

// ollamaThinkValue maps an explicit ollama_think value onto the Ollama API
// Think field. It must not receive "auto" — that is resolveOllamaThink's job.
// Unknown values fall back to false so a typo never re-enables thinking.
func ollamaThinkValue(raw string) *api.ThinkValue {
	s := strings.TrimSpace(strings.ToLower(raw))
	if s == "auto" {
		fmt.Fprintf(os.Stderr, "obey-voice: ollamaThinkValue received auto; using false (call resolveOllamaThink)\n")
		return &api.ThinkValue{Value: false}
	}
	switch s {
	case "", "false", "off", "0", "no":
		return &api.ThinkValue{Value: false}
	case "true", "on", "1", "yes":
		return &api.ThinkValue{Value: true}
	case thinkLow, thinkMedium, thinkHigh, thinkMax:
		return &api.ThinkValue{Value: s}
	default:
		fmt.Fprintf(os.Stderr, "obey-voice: invalid ollama_think %q; using false (speakable content only)\n", raw)
		return &api.ThinkValue{Value: false}
	}
}

func thinkEnabled(v *api.ThinkValue) bool {
	if v == nil || v.Value == nil {
		return false
	}
	switch x := v.Value.(type) {
	case bool:
		return x
	case string:
		return x != ""
	default:
		return false
	}
}
