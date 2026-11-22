// Package ollama processes streamed Ollama responses for the voice agent.
package ollama

import (
	"context"
	"strings"
	"unicode"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/discussion"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/textclean"
	"github.com/ollama/ollama/api"
)

// Stream sends speech and thinking separately and remembers speech already sent
// across tool calls within the same turn.
type Stream struct {
	Speak, Think func(string) error
	spoken       strings.Builder
	// breakNext puts a paragraph break ahead of the next speech. The break is
	// sent when a later round's text arrives, not when the tool call is made,
	// and it is not part of what any round said, so it stays out of spoken.
	breakNext bool
	// tailBreaks counts the line breaks the text sent so far ends on.
	tailBreaks int
}

// send writes text to the listener and notes how many line breaks it ends on.
func (s *Stream) send(text string) error {
	if text == "" {
		return nil
	}
	if err := s.Speak(text); err != nil {
		return err
	}
	body := strings.TrimRightFunc(text, unicode.IsSpace)
	breaks := strings.Count(text[len(body):], "\n")
	if body == "" {
		breaks += s.tailBreaks
	}
	s.tailBreaks = breaks
	return nil
}

// Filter consumes incremental text and releases its remaining text at end of stream.
type Filter interface {
	Feed(string) string
	Flush() string
}

// RoundOptions supplies the provider’s label and history filters and its delivery policy.
type RoundOptions struct {
	Labels, Markers      Filter
	Input, PreviousReply string
	Hold                 bool
	Discussion           *discussion.Directive
}

// Round retains both model text and delivered speech for history and partial failures.
type Round struct {
	stream        *Stream
	ctx           context.Context
	options       RoundOptions
	raw, emitted  strings.Builder
	HadThinking   bool
	outputChecked bool
	split         textclean.ThinkFilter
	toolMarkup    textclean.ToolMarkupFilter
	gate          replyPrefixGate
	runaway       runawayTextGuard
	held          []string
	ToolCalls     []api.ToolCall
	Usage         Usage
}

// NewRound starts the next model request in this turn.
func (s *Stream) NewRound(ctx context.Context, options RoundOptions) *Round {
	r := &Round{stream: s, ctx: ctx, options: options, runaway: newRunawayTextGuard(options.Input)}
	if s.spoken.Len() == 0 {
		r.gate.prior = options.PreviousReply
	}
	return r
}

func (r *Round) RawText() string     { return r.raw.String() }
func (r *Round) EmittedText() string { return r.emitted.String() }
func (r *Round) HeldText() string    { return strings.Join(r.held, "") }
func (r *Round) Repeated() bool      { return r.gate.repeat }

func (r *Round) deliver(text string) error {
	if r.options.Discussion != nil {
		if err := r.options.Discussion.State.Context().Err(); err != nil {
			return err
		}
	}
	if text == "" {
		return nil
	}
	if !r.outputChecked && r.options.Discussion != nil && r.options.Discussion.BeforeOutput != nil {
		if err := r.options.Discussion.BeforeOutput(r.ctx); err != nil {
			return err
		}
		r.outputChecked = true
	}
	sent := text
	if r.stream.breakNext {
		// The break replaces the white space the model left on either side of
		// the tool call, so the two thoughts are always one blank line apart.
		sent = strings.TrimLeftFunc(text, unicode.IsSpace)
		if err := r.stream.send(strings.Repeat("\n", max(0, 2-r.stream.tailBreaks))); err != nil {
			return err
		}
		r.stream.breakNext = sent == ""
	}
	if err := r.stream.send(sent); err != nil {
		return err
	}
	r.stream.spoken.WriteString(text)
	r.emitted.WriteString(text)
	return nil
}

func (r *Round) forward(text string) error {
	text, err := r.runaway.feed(text)
	if sendErr := r.deliver(text); sendErr != nil {
		return sendErr
	}
	return err
}

func (r *Round) emitText(text string) error {
	if r.options.Hold {
		r.held = append(r.held, text)
		return nil
	}
	return r.forward(r.gate.feed(text))
}

func (r *Round) think(text string) error {
	r.HadThinking = true
	return r.stream.Think(text)
}

func (r *Round) receive(msg api.Message) error {
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
		emit := r.options.Labels.Feed(r.options.Markers.Feed(speakable))
		if restatementSkip(r.stream.spoken.String(), r.raw.String()) {
			return nil
		}
		if emit != "" {
			if err := r.emitText(emit); err != nil {
				return err
			}
		}
	}
	if len(msg.ToolCalls) > 0 {
		r.ToolCalls = append(r.ToolCalls, msg.ToolCalls...)
	}
	return nil
}

// FlushFilters drains each text filter and preserves the first failure.
func (r *Round) FlushFilters(err error) error {
	record := func(next error) {
		if err == nil {
			err = next
		}
	}
	emit := func(text string) {
		if text != "" && !restatementSkip(r.stream.spoken.String(), r.raw.String()) {
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
		emit(r.options.Labels.Feed(r.options.Markers.Feed(speak)))
	}
	if held := r.options.Markers.Flush(); held != "" {
		emit(r.options.Labels.Feed(held))
	}
	emit(r.options.Labels.Flush())
	return err
}

// ReleaseHeld sends tool preambles to the thinking channel. Approved reply chunks go through
// the same repetition and runaway guards as incrementally streamed speech.
func (r *Round) ReleaseHeld(approved bool, err error) error {
	if !r.options.Hold {
		return err
	}
	if len(r.ToolCalls) > 0 {
		r.raw.Reset()
		if text := r.HeldText(); text != "" {
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

// Separate sends a paragraph break at once, ahead of the continuation of an
// answer that was cut off.
func (s *Stream) Separate() error { return s.send("\n\n") }

// EndThought marks the speech this round delivered as finished, because tool
// calls follow it. Whatever a later round says is a new thought and starts
// behind a paragraph break. Rounds are joined with nothing between them
// otherwise, and the reader sees "Let me look.Here it is."
func (r *Round) EndThought() {
	if strings.TrimSpace(r.emitted.String()) != "" {
		r.stream.breakNext = true
	}
}

// FinishSpeech releases speech held for repetition detection after validation.
func (r *Round) FinishSpeech(err error) error {
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
