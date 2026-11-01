package brain

import (
	"encoding/json"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/utterance"
	"regexp"
	"strings"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/chatmedia"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/continuity"
	"github.com/ollama/ollama/api"
)

// ollamaSpeakerSep marks a stable speaker id prefix on stored user messages so
// History/LoadHistory can round-trip Turn.Speaker without changing the Ollama
// wire format's role field. ASCII record separator is invalid in speech text.
const ollamaSpeakerSep = "\x1e"

// Private working-history roles distinguish quoted context from human input.
// buildMessages converts these to user-role data at the Ollama wire boundary.
const (
	ollamaSharedRole     = "shared_context"
	ollamaSharedToolRole = "shared_tool"
)

// SetSpeakerNames attaches a rename table for user-turn prompt attribution.
func (o *OllamaBrain) SetSpeakerNames(names SpeakerNames) { o.speakerNames = names }

// SetGroupParticipants replaces the group roster with a defensive copy.
func (o *OllamaBrain) SetGroupParticipants(participants []GroupParticipant) {
	o.stateMu.Lock()
	o.groupParticipants = cloneGroupParticipants(participants)
	o.stateMu.Unlock()
}

func cloneGroupParticipants(in []GroupParticipant) []GroupParticipant {
	if in == nil {
		return nil
	}
	out := make([]GroupParticipant, len(in))
	for i, participant := range in {
		out[i] = participant
		out[i].Aliases = append([]string(nil), participant.Aliases...)
	}
	return out
}

func (o *OllamaBrain) groupRoster() []GroupParticipant {
	o.stateMu.Lock()
	defer o.stateMu.Unlock()
	return cloneGroupParticipants(o.groupParticipants)
}

func (o *OllamaBrain) groupModeEnabled() bool {
	o.stateMu.Lock()
	defer o.stateMu.Unlock()
	active := 0
	for _, p := range o.groupParticipants {
		if strings.TrimSpace(p.Name) != "" {
			active++
		}
	}
	return active > 1 || o.groupHistory
}

// Warmup reads group guidance concurrently. Cache only the fact it needs;
// never let its goroutine scan mutable canonical history.
func (o *OllamaBrain) refreshGroupHistory(turns []Turn) {
	foreign := false
	for _, turn := range turns {
		if (turn.Role == "assistant" || turn.Role == "samantha") && strings.TrimSpace(turn.PersonaID) != "" && strings.TrimSpace(turn.PersonaID) != o.activePersona() {
			foreign = true
			break
		}
	}
	o.stateMu.Lock()
	o.groupHistory = foreign
	o.groupSpeakers = groupAgentNames(turns)
	o.stateMu.Unlock()
}

func (o *OllamaBrain) groupUserName(speakerID string) string {
	speakerID = strings.TrimSpace(speakerID)
	if speakerID != "" && o.speakerNames != nil {
		if name := strings.TrimSpace(o.speakerNames.Display(speakerID)); name != "" && name != speakerID {
			return name
		}
	}
	return "Unknown"
}

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
	if o.groupModeEnabled() {
		name := o.groupUserName(speakerID)
		return name + " user:\n" + content
	}
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
		groupParticipants:  o.groupRoster(),
	}
}

func (o *OllamaBrain) ClearHistory() {
	o.refreshGroupHistory(nil)
	o.resetDelegation()
	o.archive = continuity.Store{}
	o.history = nil
	o.audioProjection = nil
	o.inputTranscripts.Clear()
	o.canonical = nil
	o.working = nil
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
		return o.effectiveTurns(o.canonical)
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
		if i < len(o.working) {
			turns[i].Utterance = o.effectiveTurn(o.working[i]).Utterance
			turns[i].TypedInput = o.working[i].TypedInput
			turns[i].AudioRetained = o.working[i].AudioRetained
			if turns[i].Utterance != nil && turns[i].Utterance.Transcript.State == utterance.Ready {
				turns[i].Content = audioTranscriptProjection(o.effectiveTurn(o.working[i]))
			}
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
func (o *OllamaBrain) CanonicalHistory() []Turn { return o.effectiveTurns(o.canonical) }

// ContinuitySnapshot returns persistence history separately from the bounded
// model view. The latter is the compacted seed plus its retained tail when
// compaction has occurred.
func (o *OllamaBrain) ContinuitySnapshot() ContinuitySnapshot {
	canonical := o.canonical
	if len(canonical) == 0 {
		canonical = o.ContextHistory()
	}
	working := o.working
	if working == nil {
		working = canonical
	}
	o.stateMu.Lock()
	contextHighWater := o.contextHighWater
	o.stateMu.Unlock()
	return ContinuitySnapshot{
		Canonical:        o.effectiveTurns(canonical),
		Working:          o.effectiveTurns(working),
		Archive:          append([]continuity.Entry(nil), o.archive.Snapshot()...),
		ContextHighWater: contextHighWater,
	}
}

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
	o.refreshGroupHistory(turns)
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
	o.refreshGroupHistory(turns)
	o.loadHistoryProjection(turns, true)
	o.canonical = deepCopyTurns(turns)
	for i := range o.canonical {
		if o.canonical[i].Role == "samantha" {
			o.canonical[i].Role = "assistant"
		}
	}
}

// ImportContinuity restores the active model view without expanding a
// compacted conversation back to its canonical transcript.
func (o *OllamaBrain) ImportContinuity(snapshot ContinuitySnapshot) error {
	o.refreshGroupHistory(snapshot.Canonical)
	working := snapshot.Working
	if working == nil {
		working = snapshot.Canonical
	}
	o.loadHistoryProjectionForPersona(working, false, true)
	o.canonical = deepCopyTurns(snapshot.Canonical)
	o.archive = continuity.Store{}
	for _, entry := range snapshot.Archive {
		o.archive.Add(entry)
	}
	if o.cfg != nil {
		ceiling := o.cfg.ContinuityNumCtxCeil
		ceiling = max(o.cfg.OllamaNumCtx, min(ceiling, 32768))
		o.setContextHighWater(min(max(snapshot.ContextHighWater, o.cfg.OllamaNumCtx), ceiling))
	}
	for i := range o.canonical {
		if o.canonical[i].Role == "samantha" {
			o.canonical[i].Role = "assistant"
		}
	}
	return nil
}

func (o *OllamaBrain) loadHistoryProjection(turns []Turn, archiveProjection bool) {
	o.loadHistoryProjectionForPersona(turns, archiveProjection, false)
}

func (o *OllamaBrain) loadHistoryProjectionForPersona(turns []Turn, archiveProjection, sharedUnowned bool) {
	history := make([]api.Message, 0, len(turns))
	working := make([]Turn, 0, len(turns))
	for i, t := range turns {
		// Project/archive tool content before ownership changes its wire role.
		if t.Role == "tool" && o.cfg != nil && o.cfg.ContinuityProjectionEnabled && !strings.HasPrefix(t.Content, "[tool:") {
			if archiveProjection {
				t.Content = o.AddContinuityNote(t.Content, continuity.Provenance{ToolName: t.ToolName})
			} else {
				t.Content, _ = continuity.ProjectToolResult(t.Content, continuity.Provenance{ToolName: t.ToolName})
			}
		}
		m := o.projectTurn(t, turns, i, sharedUnowned)
		history = append(history, m)
		working = append(working, t)
	}
	o.history = sanitizeOllamaHistory(history)
	// Sanitization removes a prefix; keep the same source indices so later
	// pruning never transfers a discarded tool or drops the wrong user turn.
	o.working = deepCopyTurns(working[len(history)-len(o.history):])
}

func (o *OllamaBrain) activePersona() string {
	if o.cfg == nil {
		return ""
	}
	return strings.TrimSpace(o.cfg.ActivePersona)
}

func (o *OllamaBrain) projectTurn(t Turn, all []Turn, index int, sharedUnowned bool) api.Message {
	t = o.effectiveTurn(t)
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
		images, _ := loadImageData(t.Attachments) // Validated before a model request; assets are immutable.
		if t.Utterance != nil && t.Utterance.Audio != nil {
			if data := o.audioProjection[t.Utterance.Audio.ID]; data != nil {
				images = []api.ImageData{data}
				content = t.TypedInput
			}
			if len(images) == 0 && t.Utterance.Transcript.State == utterance.Ready {
				content = audioTranscriptProjection(t)
			}
		}
		return api.Message{Role: "user", Content: content, Images: images}
	}
	owner, name := strings.TrimSpace(t.PersonaID), strings.TrimSpace(t.PersonaDisplayName)
	legacy := role == "assistant" && legacyHistoricalAssistantHeader.MatchString(t.Content)
	if role == "tool" && owner == "" {
		owner, name, legacy = precedingToolOwner(all, index)
	} else if role == "tool" {
		_, _, legacy = precedingToolOwner(all, index)
	}
	current := o.activePersona()
	// Unbound providers cannot claim an attributed reply as their own.
	own := owner == current
	if current == "" && o.groupModeEnabled() {
		own = false
	}
	// Contaminated replies and synthetic summaries are always quoted.
	shared := !own || legacy || (sharedUnowned && index == 0 && owner == "")
	content := t.Content
	if role == "assistant" {
		content = stripLegacyHistoricalHeader(content)
	}
	if shared && role == "assistant" {
		for _, tc := range t.ToolCalls {
			content += "\nTool call: " + tc.Name + "(" + tc.Arguments + ")"
		}
		if sharedUnowned && owner == "" {
			return api.Message{Role: ollamaSharedRole, Content: sharedContextContent(content)}
		}
		return api.Message{Role: ollamaSharedRole, Content: attributedAssistantContent(name, content)}
	}
	if shared && role == "tool" {
		return api.Message{Role: ollamaSharedToolRole, Content: attributedToolContent(name, t.ToolName, content)}
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
		return "Unknown agent:\n" + content
	}
	return strings.TrimSpace(name) + " agent:\n" + content
}

func attributedToolContent(name, tool, content string) string {
	prefix := "Unknown agent tool"
	if tool != "" {
		prefix += " (" + tool + ")"
	}
	if strings.TrimSpace(name) != "" {
		prefix = strings.TrimSpace(name) + " tool"
		if tool != "" {
			prefix += " (" + tool + ")"
		}
	}
	return prefix + ":\n" + content
}

func sharedContextContent(content string) string { return "Shared conversation context:\n" + content }

// Remove complete attribution markers only from assistant output/projections.
// User quotations and the canonical transcript are left untouched.
var legacyHistoricalAssistantHeader = regexp.MustCompile(`\[Historical assistant: [^\]\r\n]+\](?:\r?\n)?`)

func stripLegacyHistoricalHeader(content string) string {
	return legacyHistoricalAssistantHeader.ReplaceAllString(content, "")
}

func deepCopyTurns(in []Turn) []Turn {
	if in == nil {
		return nil
	}
	out := make([]Turn, len(in))
	copy(out, in)
	for i := range out {
		out[i].Utterance = cloneUtterance(in[i].Utterance)
		out[i].Attachments = append([]chatmedia.Ref(nil), in[i].Attachments...)
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
	if m.Role == "assistant" && identity == nil && o.activePersona() != "" {
		t.PersonaID, t.PersonaDisplayName = o.activePersona(), o.cfg.AgentName
	}
	o.canonical = append(o.canonical, t)
	if t.Role == "assistant" && t.PersonaID != "" && t.PersonaID != o.activePersona() {
		o.stateMu.Lock()
		o.groupHistory = true
		o.stateMu.Unlock()
	}
	o.working = append(o.working, t)
	m = o.projectTurn(t, o.canonical, len(o.canonical)-1, false)
	o.history = append(o.history, m)
}

func (o *OllamaBrain) appendProjectedToolResult(tc api.ToolCall, raw string, identities ...*TurnIdentity) {
	projected := o.projectedToolResult(tc, raw)
	t := Turn{Role: "tool", Content: raw, ToolName: tc.Function.Name, ToolCallID: tc.ID, At: time.Now().UTC()}
	if len(identities) > 0 && identities[0] != nil {
		t.PersonaID, t.PersonaDisplayName = identities[0].PersonaID, identities[0].PersonaDisplayName
	}
	o.canonical = append(o.canonical, t)
	if t.Role == "assistant" && t.PersonaID != "" && t.PersonaID != o.activePersona() {
		o.stateMu.Lock()
		o.groupHistory = true
		o.stateMu.Unlock()
	}
	o.working = append(o.working, t)
	t.Content = projected.Content
	o.history = append(o.history, o.projectTurn(t, o.canonical, len(o.canonical)-1, false))
}

func (o *OllamaBrain) appendUserInput(input string, opts StreamOptions) error {
	images, err := loadImageData(opts.Attachments)
	if err != nil {
		return err
	}
	o.appendMessage(api.Message{Role: "user", Content: encodeOllamaUser(opts.Speaker, input)}, nil)
	if opts.OriginalInput != "" && opts.OriginalInput != input {
		t := &o.canonical[len(o.canonical)-1]
		t.Content, t.RoutedInput = opts.OriginalInput, input
		if len(o.working) > 0 {
			w := &o.working[len(o.working)-1]
			w.Content, w.RoutedInput = opts.OriginalInput, input
		}
	}
	o.attachInputImages(opts.Attachments, images)
	if opts.Utterance != nil {
		last := len(o.canonical) - 1
		o.canonical[last].TypedInput = input
		o.working[len(o.working)-1].TypedInput = input
		if input == "" && opts.Utterance.Transcript.State == utterance.Ready {
			o.canonical[last].Content = opts.Utterance.Transcript.Text
			o.working[len(o.working)-1].Content = opts.Utterance.Transcript.Text
		}
		o.canonical[last].Utterance = cloneUtterance(opts.Utterance)
		o.canonical[last].AudioRetained = opts.AudioRetained
		o.working[len(o.working)-1].Utterance = cloneUtterance(opts.Utterance)
		o.working[len(o.working)-1].AudioRetained = opts.AudioRetained
		if data := o.audioProjection[opts.Utterance.Audio.ID]; data != nil {
			o.history[len(o.history)-1].Images = []api.ImageData{data}
		}
	}
	return nil
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
		if ValidateTextProjection(o.effectiveTurns(o.working[:min(start, len(o.working))])) != nil {
			o.audioProjectionBlocked = true
			return
		}
		o.archiveMessages(o.history[:start])
		o.history = append([]api.Message(nil), o.history[start:]...)
		if start <= len(o.working) {
			o.working = deepCopyTurns(o.working[start:])
		}
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
		if m.Role == "user" || m.Role == ollamaSharedRole || m.Role == ollamaSharedToolRole {
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
