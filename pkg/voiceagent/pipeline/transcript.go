package pipeline

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/chatmedia"
)

// Transcript is the host-owned canonical record of a conversation. Appends
// carry the conversation epoch they belong to; an implementation drops turns
// from a conversation that has since been cleared or replaced.
type Transcript interface {
	Turns() []brain.Turn
	Append(epoch string, turns ...brain.Turn) error
	brain.ReplyMediaWriter
}

// transcriptFor returns the host transcript when provider does not keep the
// shared history itself.
func (p *Pipeline) transcriptFor(provider brain.Provider) Transcript {
	if p.Transcript == nil || provider == nil || brain.KeepsSharedHistory(provider) {
		return nil
	}
	return p.Transcript
}

func (p *Pipeline) appendTranscript(t Transcript, turns ...brain.Turn) error {
	return t.Append(p.ConversationScope().ConversationEpoch, turns...)
}

func assistantTurn(text string, identity *brain.TurnIdentity, at time.Time) brain.Turn {
	t := brain.Turn{Role: "assistant", Content: strings.TrimSpace(text), At: at}
	if identity != nil {
		t.PersonaID, t.PersonaDisplayName, t.PromptHash, t.ResponseID = identity.PersonaID, identity.PersonaDisplayName, identity.PromptHash, identity.ResponseID
	}
	return t
}

// appendReply commits a host-authored reply to the provider and, when the
// host owns the transcript, to the transcript as well.
func (p *Pipeline) appendReply(provider brain.Provider, response string, identity *brain.TurnIdentity, attachments []chatmedia.Ref) error {
	if err := appendResponse(provider, response, identity); err != nil {
		return err
	}
	t := p.transcriptFor(provider)
	if t == nil {
		return nil
	}
	reply := assistantTurn(response, identity, time.Now().UTC())
	reply.Attachments = slices.Clone(attachments)
	return p.appendTranscript(t, reply)
}

// transcriptRecording records one live provider request in the host
// transcript. wait must be called before the turn that started it returns.
type transcriptRecording struct {
	cancel context.CancelFunc
	done   chan struct{}
}

// wait stops a request the turn no longer reads and joins its recording.
func (r *transcriptRecording) wait() {
	if r == nil {
		return
	}
	r.cancel()
	<-r.done
}

// startRecordedStream starts the live provider. When the host owns the
// transcript, the request and the text delivered from it are recorded under
// the response identity once the provider finishes.
func (p *Pipeline) startRecordedStream(ctx context.Context, input string, opts brain.StreamOptions) (*brain.Stream, *transcriptRecording, error) {
	t := p.transcriptFor(p.Brain)
	if t == nil {
		stream, err := p.startBrainStream(ctx, input, opts)
		return stream, nil, err
	}
	epoch := p.ConversationScope().ConversationEpoch
	started := time.Now().UTC()
	ctx, cancel := context.WithCancel(ctx)
	stream, err := p.startBrainStream(ctx, input, opts)
	if err != nil {
		cancel()
		return nil, nil, err
	}
	rec := &transcriptRecording{cancel: cancel, done: make(chan struct{})}
	out := make(chan string, 8)
	var thinking chan string
	if stream.Thinking != nil {
		thinking = make(chan string, 8)
	}
	done := make(chan brain.StreamResult, 1)
	go func() {
		defer close(rec.done)
		defer close(done)
		if thinking != nil {
			defer close(thinking)
		}
		defer close(out)
		var delivered strings.Builder
		forwarding := true
		forward := func(ch chan<- string, v string) bool {
			if !forwarding {
				return false
			}
			select {
			case ch <- v:
				return true
			case <-ctx.Done():
				forwarding = false
				return false
			}
		}
		chunks, thinks, terminal := stream.Chunks, stream.Thinking, stream.Done
		var result brain.StreamResult
		received, hasResult := false, false
		for chunks != nil || !received {
			select {
			case v, ok := <-thinks:
				if !ok {
					thinks = nil
					continue
				}
				forward(thinking, v)
			case v, ok := <-chunks:
				if !ok {
					chunks = nil
					continue
				}
				if forward(out, v) {
					delivered.WriteString(v)
				}
			case v, ok := <-terminal:
				received, terminal = true, nil
				if ok {
					result, hasResult = v, true
				}
			}
		}
		if result.Route == "" {
			err := result.Err
			if !hasResult {
				err = errors.New("provider ended without a result")
			}
			if turns := requestTurns(input, opts, delivered.String(), err, started); len(turns) > 0 {
				_ = t.Append(epoch, turns...)
			}
		}
		if hasResult {
			done <- result
		}
	}()
	return &brain.Stream{Chunks: out, Thinking: thinking, Done: done}, rec, nil
}

// requestTurns follows the shared-history providers: the finalized input is
// the user's line, a host continuation adds none, the reply is stored in the
// same cleaned form providers store, and unfinished text keeps its disposition.
func requestTurns(input string, opts brain.StreamOptions, delivered string, err error, started time.Time) []brain.Turn {
	var turns []brain.Turn
	if !opts.AgentContinuation && !opts.RetryInput && !opts.Background {
		user := brain.Turn{Role: "user", Content: input, Speaker: opts.Speaker, At: started, Attachments: slices.Clone(opts.Attachments)}
		if opts.OriginalInput != "" {
			user.Content = opts.OriginalInput
			if input != "" && input != opts.OriginalInput {
				user.RoutedInput = input
			}
		}
		if strings.TrimSpace(user.Content) != "" || len(user.Attachments) > 0 {
			turns = append(turns, user)
		}
	}
	if reply := brain.CleanForVoice(delivered); strings.TrimSpace(reply) != "" {
		t := assistantTurn(reply, opts.Identity, time.Now().UTC())
		if err != nil {
			t.Disposition = brain.IncompleteDisposition(err)
		}
		turns = append(turns, t)
	}
	return turns
}
