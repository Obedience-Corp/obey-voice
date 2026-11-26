package brain

import (
	"encoding/json"
	"slices"
	"strings"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/utterance"

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

// humanName names the author of a stored user turn from the turn's own speaker,
// so the name never shifts with the live label of a later turn.
func (o *OllamaBrain) humanName(speakerID string) string {
	configured := ""
	if o.cfg != nil {
		configured = o.cfg.UserDisplayName
	}
	return HumanSpeakerName(speakerID, o.speakerNames, configured)
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
	if o.groupModeEnabled() {
		speakerID, content := decodeOllamaUser(stored)
		name := o.humanName(speakerID)
		if name == "User" {
			return "User:\n" + content
		}
		return name + " user:\n" + content
	}
	return o.userLine(stored)
}

// userLine is a stored user message as one attributed line: "Name: text" for
// an identified speaker, the bare text otherwise.
func (o *OllamaBrain) userLine(stored string) string {
	speakerID, content := decodeOllamaUser(stored)
	if PromptSpeaker(speakerID) == "" {
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
		jobScoped:          true,
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
	o.inputRetry = nil
	o.stopped = ollamaStopped{}
	o.refreshGroupHistory(nil)
	o.resetDelegation()
	o.archive = continuity.Store{}
	o.history = nil
	o.audioProjection = nil
	o.inputTranscripts.Clear()
	o.canonical = nil
	o.working = nil
	o.carriedData = nil
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
	o.inputRetry = nil
	o.stopped = ollamaStopped{}
	o.resetDelegation()
	// Restore starts a distinct session; never recall another session's archive.
	o.archive = continuity.Store{}
	o.setContextHighWater(0)
	o.loadHistory(turns)
}

// ImportHistory updates only the native projection. LoadHistory retains its
// explicit restore semantics and resets continuity/delegation state.
func (o *OllamaBrain) ImportHistory(turns []Turn) error {
	o.inputRetry = nil
	o.stopped = ollamaStopped{}
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
	o.inputRetry = nil
	o.stopped = ollamaStopped{}
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

// BoundHostTurns is called after the host records added messages itself, such
// as a spawn acknowledgement or a local reply. When those messages alone take
// the window past the message mark, it is trimmed as after a model reply, so
// they cannot start a compaction. A larger window that arrived with a persona
// switch is left for the next reply to trim, as before.
func (o *OllamaBrain) BoundHostTurns(added int) {
	if o.cfg == nil || o.cfg.MaxHistory <= 0 {
		return
	}
	high := o.cfg.MaxHistory * 2
	if len(o.history) > high && len(o.history)-added <= high {
		o.trimHistory()
	}
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
		if t.Role == "user" && t.PostedImages == nil {
			t.PostedImages = postedBefore(turns, i)
		}
		working = append(working, t)
	}
	o.history = sanitizeOllamaHistory(history)
	// Sanitization removes a prefix; keep the same source indices so later
	// pruning never transfers a discarded tool or drops the wrong user turn.
	o.working = deepCopyTurns(working[len(history)-len(o.history):])
	o.retireAgedImages()
}

func (o *OllamaBrain) activePersona() string {
	if o.cfg == nil {
		return ""
	}
	return strings.TrimSpace(o.cfg.ActivePersona)
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
		out[i].OpenedImages = append([]chatmedia.Ref(nil), in[i].OpenedImages...)
		out[i].PostedImages = append([]PostedImage(nil), in[i].PostedImages...)
		out[i].ToolCalls = append([]TurnToolCall(nil), in[i].ToolCalls...)
	}
	return out
}

// appendMessage is the single ownership point for live Ollama history. The
// native message is projected for the next request while the canonical turn
// retains identity and is never trimmed with that projection.
func (o *OllamaBrain) appendMessage(m api.Message, identity *TurnIdentity, disposition ...string) {
	t := Turn{Role: m.Role, Content: m.Content, ToolName: m.ToolName, ToolCallID: m.ToolCallID, At: time.Now().UTC()}
	if len(disposition) > 0 {
		t.Disposition = disposition[0]
	}
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
	if t.Role == "user" {
		t.PostedImages = postedBefore(o.working, len(o.working))
	}
	o.canonical = append(o.canonical, t)
	if t.Role == "assistant" && t.PersonaID != "" && t.PersonaID != o.activePersona() {
		o.stateMu.Lock()
		o.groupHistory = true
		o.stateMu.Unlock()
	}
	w := t
	w.PostedImages = slices.Clone(t.PostedImages)
	o.working = append(o.working, w)
	m = o.projectTurn(t, o.canonical, len(o.canonical)-1, false)
	o.history = append(o.history, m)
}

// stoppedToolResult is the result recorded for a call a stopped turn did not run.
const stoppedToolResult = "not run: the turn was stopped before this call"

// appendStoppedToolResults gives each call a stopped streamed turn did not run
// its result, so the history that turn keeps has no call without one. A
// stopped ThinkFull turn rolls its history back instead.
func (o *OllamaBrain) appendStoppedToolResults(calls []api.ToolCall, identity *TurnIdentity) {
	for _, tc := range calls {
		o.appendProjectedToolResult(tc, stoppedToolResult, identity)
	}
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
	var images []api.ImageData
	if !o.imagesWithheld {
		var err error
		if images, err = loadImageData(opts.Attachments); err != nil {
			return err
		}
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
	o.imageWindow = imageTurns
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
