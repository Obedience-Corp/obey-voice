package brain

import (
	"errors"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain/ollama"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/textclean"
	"github.com/ollama/ollama/api"
)

// singleCallRouterTools is the route schema for single_call. The model calls
// it only to hand a turn off; for conversation it answers in the same request.
// The kind enum still lists chat, and a route(chat) call is honoured.
func singleCallRouterTools(tiers *WorkerTierAvailability) api.Tools {
	description := "Hand this user turn to a worker or narrator. Use spawn only when fulfilling the request requires external actions: search the web, look something up, read or write files, run commands, or build. Spawn starts a worker that has those tools. Use narrate for long reading. Do not call route for conversation, advice, brainstorming, drafting an answer, or preferences and follow-up questions that can be answered from context; answer those directly instead. A request to suggest or describe a persona does not by itself require files or commands."
	if tiers != nil {
		description = "Hand this user turn to a worker or narrator, using this effort rubric: simple for one bounded action, medium for a few dependent steps, complex for broad or multi-stage work. Choose the effort tier that fits the request; the host will reject unavailable tiers and never substitute another tier. Use spawn only for work requiring external actions such as searches, files, or commands. Use narrate for long reading. Do not call route for conversation, advice, brainstorming, drafting an answer, or preferences and follow-up questions answerable from context; answer those directly instead. A request to suggest or describe a persona does not by itself require files or commands."
	}
	if tiers == nil || tiers.Simple || tiers.Medium || tiers.Complex {
		description += " To show or attach a new picture, choose spawn. Discussing pictures or answering questions about an attached image needs no route call; answer directly."
	}
	return routeTool(tiers, description)
}

// singleCallRound describes how one routing round of a single_call turn treats
// the model's text.
type singleCallRound struct {
	// answering rounds speak a text answer as the turn's reply.
	answering bool
	// deciding rounds were sent without the turn's activated skills, so a text
	// answer only selects chat; the reply round that follows carries them.
	deciding bool
}

// newSingleCallRound applies to a routing round of a single_call session that
// is not the private recovery request.
func newSingleCallRound(sess *toolSession, routing, recovery, skillsActivated bool) singleCallRound {
	if sess == nil || !sess.singleCall || !routing || recovery {
		return singleCallRound{}
	}
	if skillsActivated {
		return singleCallRound{deciding: true}
	}
	return singleCallRound{answering: true}
}

func (r singleCallRound) active() bool { return r.answering || r.deciding }

// textAnswer reports whether a round's text stands as a chat answer: speakable
// text and no tool call other than route(chat).
func (r singleCallRound) textAnswer(raw string, calls []api.ToolCall) bool {
	if !r.active() || !hasSpeakableContent(cleanForVoice(raw)) {
		return false
	}
	for _, call := range calls {
		if call.Function.Name != "route" || ParseRouteKind(toolCallArg(call, "kind")) != "chat" {
			return false
		}
	}
	return true
}

// markupInsteadOfCall reports a round that wrote tool-call markup as text
// before saying anything. The model meant to call a tool, so the turn asks
// the recovery request for the route instead of failing.
func (r singleCallRound) markupInsteadOfCall(err error, emitted string) bool {
	return r.active() && errors.Is(err, textclean.ErrRawToolMarkup) && !hasSpeakableContent(cleanForVoice(emitted))
}

func (r singleCallRound) purpose(round string) string {
	if r.answering && round == PurposeRoute {
		return PurposeRouteReply
	}
	return round
}

// settle reads a finished routing round. Markup written instead of a call
// becomes a missing route, so the recovery request decides. Text that stands
// as the answer drops a route(chat) beside it, which asks for nothing more. A
// deciding round's text only selects chat: the reply round that follows
// carries the activated skills.
func (r singleCallRound) settle(output *ollama.Round, agentName string, err error) (bool, error) {
	if r.markupInsteadOfCall(err, output.EmittedText()) {
		err = nil
	}
	chatText := err == nil && r.textAnswer(stripHistoricalAssistantMarkers(stripReplyConventions(agentName, output.RawText())), output.ToolCalls)
	if chatText {
		output.ToolCalls = nil
	}
	return chatText, err
}
