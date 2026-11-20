package brain

import (
	"context"
	"strings"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/discussion"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/textclean"
	"github.com/ollama/ollama/api"
)

// streamRoundOutput filters one model response. The spoken transcript spans
// tool rounds so a later response can suppress repeated speech.
type streamRoundOutput struct {
	ctx           context.Context
	out, thinking chan<- string
	discussion    *discussion.Directive
	spoken        *strings.Builder
	emitted, raw  strings.Builder
	hadThinking   bool
	outputChecked bool

	strip      *labelStripper
	markers    historicalAssistantMarkerFilter
	split      textclean.ThinkFilter
	toolMarkup textclean.ToolMarkupFilter
	gate       replyPrefixGate
	runaway    runawayTextGuard

	hold      bool
	held      []string
	toolCalls []api.ToolCall
}

func (r *streamRoundOutput) deliver(text string) error {
	if r.discussion != nil {
		if err := r.discussion.State.Context().Err(); err != nil {
			return err
		}
	}
	if text == "" {
		return nil
	}
	if !r.outputChecked && r.discussion != nil && r.discussion.BeforeOutput != nil {
		if err := r.discussion.BeforeOutput(r.ctx); err != nil {
			return err
		}
		r.outputChecked = true
	}
	if err := sendChunk(r.ctx, r.out, text); err != nil {
		return err
	}
	r.spoken.WriteString(text)
	r.emitted.WriteString(text)
	return nil
}

func (r *streamRoundOutput) forward(text string) error {
	text, err := r.runaway.feed(text)
	if sendErr := r.deliver(text); sendErr != nil {
		return sendErr
	}
	return err
}

func (r *streamRoundOutput) emitText(text string) error {
	if r.hold {
		r.held = append(r.held, text)
		return nil
	}
	return r.forward(r.gate.feed(text))
}

func (r *streamRoundOutput) think(text string) error {
	r.hadThinking = true
	return sendChunk(r.ctx, r.thinking, text)
}

func (r *streamRoundOutput) receive(msg api.Message) error {
	fieldThink := msg.Thinking
	if fieldThink != "" {
		if err := r.think(fieldThink); err != nil {
			return err
		}
	}
	if msg.Content != "" {
		r.raw.WriteString(msg.Content)
		speakable, inner := r.split.FeedSplit(msg.Content)
		if inner != "" && fieldThink == "" {
			if err := r.think(inner); err != nil {
				return err
			}
		}
		// emitText holds live routing content until its role is
		// known; ordinary replies and worker preambles stream.
		speakable = r.toolMarkup.Feed(speakable)
		if err := r.toolMarkup.Err(); err != nil {
			return err
		}
		emit := r.strip.Feed(r.markers.Feed(speakable))
		if restatementSkip(r.spoken.String(), r.raw.String()) {
			return nil
		}
		if emit != "" {
			if err := r.emitText(emit); err != nil {
				return err
			}
		}
	}
	if len(msg.ToolCalls) > 0 {
		r.toolCalls = append(r.toolCalls, msg.ToolCalls...)
	}
	return nil
}

func (r *streamRoundOutput) flushFilters(err error) error {
	// Flush each filter in order, even after a transport or delivery failure.
	record := func(next error) {
		if err == nil {
			err = next
		}
	}
	emit := func(text string) {
		if text != "" && !restatementSkip(r.spoken.String(), r.raw.String()) {
			record(r.emitText(text))
		}
	}
	speak, think := r.split.FlushSplit()
	if think != "" {
		record(r.think(think))
	}
	speak = r.toolMarkup.Feed(speak) + r.toolMarkup.Flush()
	record(r.toolMarkup.Err())
	if speak != "" {
		emit(r.strip.Feed(r.markers.Feed(speak)))
	}
	if held := r.markers.Flush(); held != "" {
		emit(r.strip.Feed(held))
	}
	emit(r.strip.Flush())
	return err
}

// Tool preambles go to the thinking channel. Approved reply chunks go through
// the same repetition and runaway guards as incrementally streamed speech.
func (r *streamRoundOutput) releaseHeld(approved bool, err error) error {
	if !r.hold {
		return err
	}
	if len(r.toolCalls) > 0 {
		r.raw.Reset()
		if text := strings.Join(r.held, ""); text != "" {
			if sendErr := r.think(text); err == nil {
				err = sendErr
			}
		}
	} else if approved && err == nil {
		for _, text := range r.held {
			if err := r.forward(r.gate.feed(text)); err != nil {
				return err
			}
		}
	}
	return err
}

func (r *streamRoundOutput) finishSpeech(err error) error {
	if err == nil {
		err = r.forward(r.gate.release())
	}
	if tail := r.runaway.flush(); tail != "" {
		if sendErr := r.deliver(tail); err == nil {
			err = sendErr
		}
	}
	return err
}
