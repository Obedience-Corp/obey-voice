package pipeline

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/chatmedia"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/nativeaudio"
)

const nativeReplyLimit = 256 << 10

var ErrNativeReplyLimit = errors.New("native reply exceeds 256 KiB; speech was not started")

// NativePolicy is prepared with the selected persona binding. Nil preserves
// Standard streaming synthesis. It never constructs a second conversation loop.
type NativePolicy struct {
	captionMu      sync.Mutex
	caption        *nativeCaptionTicket
	Lifetime       context.Context
	PublishCaption func(context.Context, func() error) error
	retired        atomic.Bool
	Controls       string
	Transcript     string
	RetainAudio    bool
	Media          *chatmedia.AudioSession
	Store          *chatmedia.Store
	Client         *nativeaudio.Client
	Scheduler      *nativeaudio.Scheduler
}

// collectNativeReply displays real deltas but admits no speech until model and
// tools report successful completion. A recovered partial is still a failure.
func (p *Pipeline) collectNativeReply(ctx context.Context, cancelTurn context.CancelFunc, stream *brain.Stream, metrics *turnMetrics) (string, brain.StreamResult, error) {
	if stream == nil || stream.Done == nil {
		cancelTurn()
		return "", brain.StreamResult{}, errors.New("native chat requires a terminal result")
	}
	observerCtx, cancelObserver := context.WithCancel(ctx)
	out, _, observerDone := p.observeStream(observerCtx, stream, metrics)
	defer func() { cancelObserver(); <-observerDone }()
	var reply strings.Builder
	for chunk := range out {
		if len(chunk) > nativeReplyLimit-reply.Len() {
			cancelTurn()
			cancelObserver()
			p.joinNativeProducer(stream)
			return reply.String(), brain.StreamResult{}, ErrNativeReplyLimit
		}
		reply.WriteString(chunk)
	}
	<-observerDone
	select {
	case result, ok := <-stream.Done:
		if !ok {
			return reply.String(), result, errors.New("native chat closed without a terminal result")
		}
		if result.Err != nil {
			return reply.String(), result, result.Err
		}
		if !utf8.ValidString(reply.String()) {
			return reply.String(), result, errors.New("native reply is not valid UTF-8")
		}
		return reply.String(), result, nil
	case <-ctx.Done():
		cancelTurn()
		p.joinNativeProducer(stream)
		return reply.String(), brain.StreamResult{}, ctx.Err()
	}
}
func (p *Pipeline) joinNativeProducer(stream *brain.Stream) {
	timer := time.NewTimer(nativeaudio.CaptionJoinTimeout)
	defer timer.Stop()
	select {
	case <-stream.Done:
	case <-timer.C:
		if p.Native != nil && p.Native.Scheduler != nil {
			p.Native.Scheduler.MarkUnavailable()
		}
	}
}
