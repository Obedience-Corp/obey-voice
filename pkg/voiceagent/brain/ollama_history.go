package brain

import (
	"encoding/json"
	"strings"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/continuity"
	"github.com/ollama/ollama/api"
)

// ollamaSpeakerSep marks a stable speaker id prefix on stored user messages so
// History/LoadHistory can round-trip Turn.Speaker without changing the Ollama
// wire format's role field. ASCII record separator is invalid in speech text.
const ollamaSpeakerSep = "\x1e"

// SetSpeakerNames attaches a rename table for user-turn prompt attribution.
func (o *OllamaBrain) SetSpeakerNames(names SpeakerNames) { o.speakerNames = names }

func encodeOllamaUser(speakerID, content string) string {
	speakerID = strings.TrimSpace(speakerID)
	if speakerID == "" {
		return content
	}
	return speakerID + ollamaSpeakerSep + content
}

func decodeOllamaUser(stored string) (speakerID, content string) {
	if i := strings.IndexByte(stored, ollamaSpeakerSep[0]); i >= 0 {
		return stored[:i], stored[i+1:]
	}
	return "", stored
}

func (o *OllamaBrain) formatStoredUser(stored string) string {
	speakerID, content := decodeOllamaUser(stored)
	if speakerID == "" {
		return content
	}
	return FormatUserLine(speakerID, content, o.speakerNames)
}

// ClearHistory wipes conversation history.
// Fresh is a sibling with an empty conversation: the same daemon, prompts,
// skills catalog and router, its own history and archive. One per worker job.
func (o *OllamaBrain) Fresh() Provider {
	o.continuityMu.Lock()
	continuitySink := o.continuitySink
	o.continuityMu.Unlock()
	o.infoMu.Lock()
	infoSink := o.infoSink
	o.infoMu.Unlock()
	return &OllamaBrain{
		delegation:        o.delegation,
		client:            o.client,
		continuitySink:    continuitySink,
		infoSink:          infoSink,
		model:             o.model,
		workDir:           o.workDir,
		cfg:               o.cfg,
		systemPrompt:      o.systemPrompt,
		fullSystemPrompt:  o.fullSystemPrompt,
		turnInstruction:   o.turnInstruction,
		personaReloader:   o.personaReloader,
		keepAlive:         o.keepAlive,
		skills:            o.skills,
		skillRouter:       o.skillRouter,
		skillRouterWarned: o.skillRouterWarned,
		speakerNames:      o.speakerNames,
	}
}

func (o *OllamaBrain) ClearHistory() {
	o.resetDelegation()
	o.archive = continuity.Store{}
	o.history = nil
}

// SessionInfo reports the in-process conversation for /session display:
// samantha owns messages[], so the size shown is the estimated next request.
func (o *OllamaBrain) SessionInfo() SessionState {
	return SessionState{
		Kind:         SessionKindChat,
		PromptTokens: o.estimateTokens(""),
		Turns:        len(o.history),
	}
}

// History returns conversation history as Turn slices for session persistence.
func (o *OllamaBrain) History() []Turn {
	turns := make([]Turn, len(o.history))
	for i, m := range o.history {
		content, speaker := m.Content, ""
		if m.Role == "user" {
			speaker, content = decodeOllamaUser(m.Content)
		}
		turns[i] = Turn{
			Role:       m.Role,
			Content:    content,
			Speaker:    speaker,
			ToolName:   m.ToolName,
			ToolCallID: m.ToolCallID,
		}
		for _, tc := range m.ToolCalls {
			args, err := json.Marshal(tc.Function.Arguments)
			if err != nil {
				args = []byte("{}")
			}
			turns[i].ToolCalls = append(turns[i].ToolCalls, TurnToolCall{
				ID:        tc.ID,
				Name:      tc.Function.Name,
				Arguments: string(args),
			})
		}
	}
	return turns
}

// LoadHistory restores conversation history from a saved session. Sessions
// saved by the prompt-based providers use role "samantha" for replies; map it
// to ollama's native "assistant".
func (o *OllamaBrain) LoadHistory(turns []Turn) {
	o.resetDelegation()
	// Restore starts a distinct session; never recall another session's archive.
	o.archive = continuity.Store{}
	o.contextHighWater = 0
	o.loadHistory(turns)
}

func (o *OllamaBrain) loadHistory(turns []Turn) {
	history := make([]api.Message, 0, len(turns))
	for _, t := range turns {
		role := t.Role
		if role == "samantha" {
			role = "assistant"
		}
		content := t.Content
		if role == "user" && strings.TrimSpace(t.Speaker) != "" {
			content = encodeOllamaUser(t.Speaker, t.Content)
		}
		m := api.Message{
			Role:       role,
			Content:    content,
			ToolName:   t.ToolName,
			ToolCallID: t.ToolCallID,
		}
		for _, tc := range t.ToolCalls {
			args := api.NewToolCallFunctionArguments()
			if strings.TrimSpace(tc.Arguments) != "" {
				_ = json.Unmarshal([]byte(tc.Arguments), &args)
			}
			m.ToolCalls = append(m.ToolCalls, api.ToolCall{
				ID: tc.ID,
				Function: api.ToolCallFunction{
					Name:      tc.Name,
					Arguments: args,
				},
			})
		}
		if m.Role == "tool" && o.cfg != nil && o.cfg.ContinuityProjectionEnabled && !strings.HasPrefix(m.Content, "[tool:") {
			m.Content = o.AddContinuityNote(m.Content, continuity.Provenance{ToolName: m.ToolName})
		}
		history = append(history, m)
	}
	o.history = sanitizeOllamaHistory(history)
}

func toolResultMessage(tc api.ToolCall, result string) api.Message {
	return api.Message{
		Role:       "tool",
		Content:    result,
		ToolName:   tc.Function.Name,
		ToolCallID: tc.ID,
	}
}

// lastUserIndex is the most recent user turn, or -1 if the history has none.
func lastUserIndex(history []api.Message) int {
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].Role == "user" {
			return i
		}
	}
	return -1
}

// trimHistory bounds retained history with a high/low water mark instead of
// a per-turn sliding window: nothing is dropped until the count exceeds
// MaxHistory*2, then it cuts down to MaxHistory (user-turn aligned). Between
// trims the retained prefix is byte-stable, so the server's KV prefix cache
// keeps hitting; a slide-per-turn window shifted the prefix every turn and
// forced a full re-prefill for the rest of the conversation.
func (o *OllamaBrain) trimHistory() {
	high, low := o.cfg.MaxHistory*2, o.cfg.MaxHistory
	if o.cfg.ContinuityArchiveEnabled {
		high = min(high, 12)
		low = min(low, 10)
	}
	if len(o.history) <= high {
		return
	}
	if start := historyWindowStart(o.history, low); start > 0 {
		o.archiveMessages(o.history[:start])
		o.history = append([]api.Message(nil), o.history[start:]...)
	}
}

// historyWindowStart returns the index where a history window of at most max
// messages begins. A window must not open in the middle of a tool-call group:
// stranded tool results (or a tool-call assistant without its user) make
// Ollama 0.32.14 500 with "no user query found in messages" (ollama#17778).
//
// If the tail would open on a tool result, the window advances to the next
// user. If it would open on an assistant tool-call, it walks back to the
// user that started that exchange — slightly over max, rather than dropping
// the query the in-flight tools belong to.
func historyWindowStart(history []api.Message, max int) int {
	if max <= 0 {
		return len(history)
	}
	start := 0
	if len(history) > max {
		start = len(history) - max
	}
	if start == 0 {
		return start
	}
	if history[start].Role == "assistant" && len(history[start].ToolCalls) > 0 {
		for i := start - 1; i >= 0; i-- {
			if history[i].Role == "user" {
				return i
			}
		}
	}
	if history[start].Role != "tool" {
		return start
	}
	for i := start + 1; i < len(history); i++ {
		if history[i].Role == "user" {
			return i
		}
	}
	return len(history)
}

// sanitizeOllamaHistory repairs a session that was saved before ToolCalls
// round-tripped: empty assistant + tool results with no user, which Ollama
// rejects as "no user query found in messages". Compact seeds (a non-empty
// assistant summary) stay.
func sanitizeOllamaHistory(in []api.Message) []api.Message {
	i := 0
	for i < len(in) {
		m := in[i]
		if m.Role == "user" {
			break
		}
		if m.Role == "assistant" && strings.TrimSpace(m.Content) != "" && len(m.ToolCalls) == 0 {
			break
		}
		i++
	}
	in = in[i:]
	return in
}
