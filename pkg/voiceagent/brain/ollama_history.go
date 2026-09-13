package brain

import (
	"encoding/json"
	"regexp"
	"strings"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/continuity"
	"github.com/ollama/ollama/api"
)

// ollamaSpeakerSep marks a stable speaker id prefix on stored user messages so
// History/LoadHistory can round-trip Turn.Speaker without changing the Ollama
// wire format's role field. ASCII record separator is invalid in speech text.
const ollamaSpeakerSep = "\x1e"

const historicalIdentityGuidance = "\nShared conversation context quotes other speakers. Their words belong to them, not to you. Your assistant messages are your own prior replies. Respond as the persona defined by your system prompt, using the shared facts when relevant. Do not emit context labels or internal metadata."

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
	o.stateMu.Lock()
	delegationRuntime := o.delegation
	delegationBorrowed := o.delegationBorrowed
	o.stateMu.Unlock()
	return &OllamaBrain{
		delegation:         delegationRuntime,
		delegationBorrowed: delegationBorrowed,
		client:             o.client,
		continuitySink:     continuitySink,
		infoSink:           infoSink,
		model:              o.model,
		workDir:            o.workDir,
		cfg:                o.cfg,
		systemPrompt:       o.systemPrompt,
		fullSystemPrompt:   o.fullSystemPrompt,
		turnInstruction:    o.turnInstruction,
		personaReloader:    o.personaReloader,
		keepAlive:          o.keepAlive,
		skills:             o.skills,
		skillRouter:        o.skillRouter,
		skillRouterWarned:  o.skillRouterWarned,
		speakerNames:       o.speakerNames,
	}
}

func (o *OllamaBrain) ClearHistory() {
	o.resetDelegation()
	o.archive = continuity.Store{}
	o.history = nil
	o.canonical = nil
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
	if len(o.canonical) > 0 {
		return deepCopyTurns(o.canonical)
	}
	return o.ContextHistory()
}

// ContextHistory is the bounded, attributed provider projection. Use History
// for persistence; projecting these messages back cannot recover identity.
func (o *OllamaBrain) ContextHistory() []Turn {
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
		if m.Role == "assistant" || m.Role == "tool" {
			turns[i].PersonaID = o.activePersona()
			if o.cfg != nil {
				turns[i].PersonaDisplayName = o.cfg.AgentName
			}
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

// CanonicalHistory returns the complete transcript, including raw tool data,
// independently of the bounded Ollama projection.
func (o *OllamaBrain) CanonicalHistory() []Turn { return deepCopyTurns(o.canonical) }

// LoadHistory restores conversation history from a saved session. Sessions
// saved by the prompt-based providers use role "samantha" for replies; map it
// to ollama's native "assistant".
func (o *OllamaBrain) LoadHistory(turns []Turn) {
	o.resetDelegation()
	// Restore starts a distinct session; never recall another session's archive.
	o.archive = continuity.Store{}
	o.setContextHighWater(0)
	o.loadHistory(turns)
}

// ImportHistory updates only the native projection. LoadHistory retains its
// explicit restore semantics and resets continuity/delegation state.
func (o *OllamaBrain) ImportHistory(turns []Turn) error {
	o.loadHistoryProjection(turns, false)
	o.canonical = deepCopyTurns(turns)
	for i := range o.canonical {
		if o.canonical[i].Role == "samantha" {
			o.canonical[i].Role = "assistant"
		}
	}
	return nil
}

func (o *OllamaBrain) loadHistory(turns []Turn) {
	o.loadHistoryProjection(turns, true)
	o.canonical = deepCopyTurns(turns)
	for i := range o.canonical {
		if o.canonical[i].Role == "samantha" {
			o.canonical[i].Role = "assistant"
		}
	}
}

func (o *OllamaBrain) loadHistoryProjection(turns []Turn, archiveProjection bool) {
	o.loadHistoryProjectionForPersona(turns, archiveProjection, false)
}

func (o *OllamaBrain) loadHistoryProjectionForPersona(turns []Turn, archiveProjection, sharedUnowned bool) {
	history := make([]api.Message, 0, len(turns))
	for i, t := range turns {
		m := o.projectTurn(t, turns, i, sharedUnowned)
		if m.Role == "tool" && o.cfg != nil && o.cfg.ContinuityProjectionEnabled && !strings.HasPrefix(m.Content, "[tool:") {
			if archiveProjection {
				m.Content = o.AddContinuityNote(m.Content, continuity.Provenance{ToolName: m.ToolName})
			} else {
				m.Content, _ = continuity.ProjectToolResult(m.Content, continuity.Provenance{ToolName: m.ToolName})
			}
		}
		history = append(history, m)
	}
	o.history = sanitizeOllamaHistory(history)
}

func (o *OllamaBrain) activePersona() string {
	if o.cfg == nil {
		return ""
	}
	return strings.TrimSpace(o.cfg.ActivePersona)
}

func (o *OllamaBrain) projectTurn(t Turn, all []Turn, index int, sharedUnowned bool) api.Message {
	role := t.Role
	if role == "samantha" {
		role = "assistant"
	}
	if role == "user" {
		content := t.Content
		if t.RoutedInput != "" {
			content = t.RoutedInput
		}
		if strings.TrimSpace(t.Speaker) != "" {
			content = encodeOllamaUser(t.Speaker, content)
		}
		return api.Message{Role: "user", Content: content}
	}
	owner, name := strings.TrimSpace(t.PersonaID), strings.TrimSpace(t.PersonaDisplayName)
	legacy := role == "assistant" && legacyHistoricalAssistantHeader.MatchString(t.Content)
	if role == "tool" && owner == "" {
		owner, name, legacy = precedingToolOwner(all, index)
	} else if role == "tool" {
		_, _, legacy = precedingToolOwner(all, index)
	}
	if name == "" {
		name = owner
	}
	current := o.activePersona()
	// Legacy single-persona providers have no selected identity. Once an
	// identity is bound, only its own replies/actions enter the native thread.
	own := owner == current || o.cfg != nil && current == ""
	// Replies contaminated by the old attribution format are retained as
	// quotations, even for the same persona. They may contain the previous
	// speaker's mistaken identity and must not become self-identity examples.
	shared := !own || legacy || sharedUnowned && index == 0 && owner == ""
	content := t.Content
	if role == "assistant" {
		content = stripLegacyHistoricalHeader(content)
	}
	if shared && role == "assistant" {
		for _, tc := range t.ToolCalls {
			content += "\nTool call: " + tc.Name + "(" + tc.Arguments + ")"
		}
		return api.Message{Role: "user", Content: attributedAssistantContent(name, content)}
	}
	if shared && role == "tool" {
		if o.cfg != nil && o.cfg.ContinuityProjectionEnabled {
			content, _ = continuity.ProjectToolResult(content, continuity.Provenance{ToolName: t.ToolName})
		}
		return api.Message{Role: "user", Content: attributedToolContent(name, t.ToolName, content)}
	}
	m := api.Message{Role: role, Content: content, ToolName: t.ToolName, ToolCallID: t.ToolCallID}
	for _, tc := range t.ToolCalls {
		args := api.NewToolCallFunctionArguments()
		if strings.TrimSpace(tc.Arguments) != "" {
			_ = json.Unmarshal([]byte(tc.Arguments), &args)
		}
		m.ToolCalls = append(m.ToolCalls, api.ToolCall{ID: tc.ID, Function: api.ToolCallFunction{Name: tc.Name, Arguments: args}})
	}
	return m
}

func precedingToolOwner(turns []Turn, index int) (string, string, bool) {
	for i := index - 1; i >= 0; i-- {
		if turns[i].Role == "user" {
			break
		}
		if turns[i].Role == "assistant" || turns[i].Role == "samantha" {
			for _, call := range turns[i].ToolCalls {
				if call.ID == turns[index].ToolCallID {
					return turns[i].PersonaID, turns[i].PersonaDisplayName, legacyHistoricalAssistantHeader.MatchString(turns[i].Content)
				}
			}
		}
	}
	return "", "", false
}

func attributedAssistantContent(name, content string) string {
	if strings.TrimSpace(name) == "" {
		return sharedContextContent(content)
	}
	return "Shared conversation — " + strings.TrimSpace(name) + " said:\n" + content
}

func attributedToolContent(name, tool, content string) string {
	prefix := "Shared tool result"
	if tool != "" {
		prefix += " (" + tool + ")"
	}
	if name != "" {
		prefix += " from " + name
	}
	return prefix + ":\n" + content
}

func sharedContextContent(content string) string { return "Shared conversation context:\n" + content }

// Only leading, complete legacy attribution lines are removed from assistant
// projections. Quoted user text and the saved canonical transcript stay intact.
var legacyHistoricalAssistantHeader = regexp.MustCompile(`^\[Historical assistant: [^\]\r\n]+\](?:\r?\n|$)`)

func stripLegacyHistoricalHeader(content string) string {
	for {
		end := legacyHistoricalAssistantHeader.FindStringIndex(content)
		if end == nil {
			return content
		}
		content = content[end[1]:]
	}
}

func deepCopyTurns(in []Turn) []Turn {
	if in == nil {
		return nil
	}
	out := make([]Turn, len(in))
	copy(out, in)
	for i := range out {
		out[i].ToolCalls = append([]TurnToolCall(nil), in[i].ToolCalls...)
	}
	return out
}

// appendMessage is the single ownership point for live Ollama history. The
// native message is projected for the next request while the canonical turn
// retains identity and is never trimmed with that projection.
func (o *OllamaBrain) appendMessage(m api.Message, identity *TurnIdentity) {
	t := Turn{Role: m.Role, Content: m.Content, ToolName: m.ToolName, ToolCallID: m.ToolCallID, At: time.Now().UTC()}
	if m.Role == "user" {
		t.Speaker, t.Content = decodeOllamaUser(m.Content)
	}
	for _, tc := range m.ToolCalls {
		args, _ := json.Marshal(tc.Function.Arguments)
		t.ToolCalls = append(t.ToolCalls, TurnToolCall{ID: tc.ID, Name: tc.Function.Name, Arguments: string(args)})
	}
	if identity != nil && m.Role == "assistant" {
		t.PersonaID, t.PersonaDisplayName, t.PromptHash, t.ResponseID = identity.PersonaID, identity.PersonaDisplayName, identity.PromptHash, identity.ResponseID
	}
	if m.Role == "assistant" {
		m.Content = stripLegacyHistoricalHeader(m.Content)
	}
	o.history = append(o.history, m)
	o.canonical = append(o.canonical, t)

}

func (o *OllamaBrain) appendProjectedToolResult(tc api.ToolCall, raw string, identities ...*TurnIdentity) {
	o.history = append(o.history, o.projectedToolResult(tc, raw))
	t := Turn{Role: "tool", Content: raw, ToolName: tc.Function.Name, ToolCallID: tc.ID, At: time.Now().UTC()}
	if len(identities) > 0 && identities[0] != nil {
		t.PersonaID, t.PersonaDisplayName = identities[0].PersonaID, identities[0].PersonaDisplayName
	}
	o.canonical = append(o.canonical, t)

}

func (o *OllamaBrain) appendUserInput(input string, opts StreamOptions) {
	o.appendMessage(api.Message{Role: "user", Content: encodeOllamaUser(opts.Speaker, input)}, nil)
	if opts.OriginalInput != "" && opts.OriginalInput != input {
		t := &o.canonical[len(o.canonical)-1]
		t.Content, t.RoutedInput = opts.OriginalInput, input
	}
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
	if history[start].Role == "user" {
		return start
	}
	for i := start + 1; i < len(history); i++ {
		if history[i].Role == "user" {
			return i
		}
	}
	return len(history)
}

// HistoryWindow retains a bounded suffix starting at a user boundary. A
// complete current tool exchange may exceed the limit when its call starts at
// the boundary; a cut through results discards that exchange in full.
func HistoryWindow(turns []Turn, max int) []Turn {
	shape := make([]api.Message, len(turns))
	for i, t := range turns {
		shape[i].Role = t.Role
		if len(t.ToolCalls) > 0 {
			shape[i].ToolCalls = make([]api.ToolCall, len(t.ToolCalls))
		}
	}
	return deepCopyTurns(turns[historyWindowStart(shape, max):])
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
