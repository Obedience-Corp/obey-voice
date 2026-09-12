package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/pipeline"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/session"
)

// recoveryArtifact keeps the three authorities visible around one compact:
// canonical transcript, bounded provider context, and durable session/backup.
// Canonical is always read through CanonicalHistoryProvider when available;
// Context is the provider's model projection and is never persisted as the
// conversation authority.
type recoveryArtifact struct {
	Stage           string                `json:"stage"`
	CapturedAt      time.Time             `json:"captured_at"`
	Persona         eventsPersonaSnapshot `json:"persona"`
	CompactPrompt   string                `json:"compact_prompt"`
	CanonicalSource string                `json:"canonical_source"`
	ContextSource   string                `json:"context_source"`
	Canonical       []brain.Turn          `json:"canonical"`
	Context         []brain.Turn          `json:"context"`
	Disk            *session.Session      `json:"disk,omitempty"`
	Backup          *session.Session      `json:"backup,omitempty"`
	BackupPath      string                `json:"backup_path,omitempty"`
}

// This small JSON-only projection avoids making the artifact depend on the
// event package's larger selection shape while retaining the identity fields
// needed to review historical identity and an A→B→A run.
type eventsPersonaSnapshot struct {
	PersonaID         string `json:"persona_id"`
	DisplayName       string `json:"display_name"`
	SessionID         string `json:"session_id"`
	ConversationEpoch string `json:"conversation_epoch"`
}

func (r *personaRuntime) compact(ctx context.Context, p *pipeline.Pipeline) error {
	if r.selector == nil || r.store == nil || r.session == nil {
		return fmt.Errorf("compaction: runtime is not configured")
	}
	cfg := r.selector.Config()
	prompt, err := brain.CompactInstruction(cfg)
	if err != nil {
		return fmt.Errorf("compaction prompt: %w", err)
	}
	before, err := r.captureRecovery("before", p.Brain, prompt)
	if err != nil {
		return err
	}
	r.mu.Lock()
	r.recovery = append(r.recovery, before)
	r.mu.Unlock()

	previousBackup := p.OnCompactBackup
	backup := r.backupFn
	if backup == nil {
		backup = func(turns []brain.Turn) error { return r.store.SaveBackup(r.session, turns) }
	}
	previousPrompt := p.CompactPrompt
	defer func() {
		p.OnCompactBackup = previousBackup
		p.CompactPrompt = previousPrompt
	}()
	p.OnCompactBackup = func(turns []brain.Turn) {
		if previousBackup != nil {
			previousBackup(turns)
		}
		if saveErr := backup(turns); saveErr != nil {
			r.recordSaveError(fmt.Errorf("compact backup: %w", saveErr))
		}
	}
	p.CompactPrompt = prompt
	err = p.CompactConversation(ctx)
	if err != nil {
		return fmt.Errorf("compact: %w", err)
	}
	after, captureErr := r.captureRecovery("after", p.Brain, prompt)
	if captureErr != nil {
		return captureErr
	}
	if after.Backup == nil {
		return fmt.Errorf("compact persistence: pre-compact backup is missing after compact")
	}
	r.mu.Lock()
	saveErr := r.saveErr
	r.mu.Unlock()
	if saveErr != nil {
		return fmt.Errorf("compact persistence: %w", saveErr)
	}
	r.mu.Lock()
	r.recovery = append(r.recovery, after)
	r.mu.Unlock()
	return nil
}

func (r *personaRuntime) captureRecovery(stage string, provider brain.Provider, prompt string) (recoveryArtifact, error) {
	if provider == nil {
		return recoveryArtifact{}, fmt.Errorf("recovery %s: provider is nil", stage)
	}
	canonical := append([]brain.Turn(nil), provider.History()...)
	canonicalSource := "history"
	if source, ok := provider.(brain.CanonicalHistoryProvider); ok {
		canonical = source.CanonicalHistory()
		canonicalSource = "canonical_history_provider"
	}
	contextTurns := append([]brain.Turn(nil), provider.History()...)
	contextSource := "history"
	if source, ok := provider.(brain.ContextHistoryProvider); ok {
		contextTurns = source.ContextHistory()
		contextSource = "context_history_provider"
	}
	row := recoveryArtifact{
		Stage: stage, CapturedAt: time.Now().UTC(), CompactPrompt: prompt,
		CanonicalSource: canonicalSource, ContextSource: contextSource,
		Canonical: canonical, Context: contextTurns,
		Persona: eventsPersonaSnapshotFrom(r.selector.Snapshot()),
	}
	if r.store != nil && r.session != nil {
		loaded, err := r.store.Load(r.session.ID)
		if err != nil {
			return recoveryArtifact{}, fmt.Errorf("recovery %s disk reload: %w", stage, err)
		}
		row.Disk = loaded
	}
	path := filepath.Join(r.sessionDir, r.session.ID+".pre-compact.json")
	if data, err := os.ReadFile(path); err == nil {
		var backup session.Session
		if err := json.Unmarshal(data, &backup); err != nil {
			return recoveryArtifact{}, fmt.Errorf("recovery %s backup decode: %w", stage, err)
		}
		row.Backup = &backup
		row.BackupPath = path
	} else if !os.IsNotExist(err) {
		return recoveryArtifact{}, fmt.Errorf("recovery %s backup read: %w", stage, err)
	}
	return row, nil
}

func eventsPersonaSnapshotFrom(selection events.PersonaSelection) eventsPersonaSnapshot {
	return eventsPersonaSnapshot{PersonaID: selection.PersonaID, DisplayName: selection.DisplayName, SessionID: selection.SessionID, ConversationEpoch: selection.ConversationEpoch}
}

func (r *personaRuntime) writeRecoveryArtifacts(write func(string, any) error) error {
	r.mu.Lock()
	rows := append([]recoveryArtifact(nil), r.recovery...)
	r.mu.Unlock()
	if len(rows) == 0 {
		return nil
	}
	for _, row := range rows {
		if err := write("recovery-"+row.Stage+".json", row); err != nil {
			return err
		}
	}
	return write("recovery-artifacts.json", rows)
}

func (r *liveRunner) compact(ctx context.Context) error {
	if r.persona == nil || r.agent == nil || r.agent.Pipeline == nil {
		return fmt.Errorf("compaction: persona agent is not configured")
	}
	return r.persona.compact(ctx, r.agent.Pipeline)
}

// importCanonicalHistory seeds only the provider projection and canonical
// transcript. It deliberately avoids Provider.LoadHistory, whose public
// contract resets a live provider session.
func (r *liveRunner) importCanonicalHistory(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("history fixture %s: %w", path, err)
	}
	var turns []brain.Turn
	if err := json.Unmarshal(data, &turns); err != nil {
		return fmt.Errorf("history fixture %s: decode: %w", path, err)
	}
	if len(turns) == 0 {
		return fmt.Errorf("history fixture %s: no turns", path)
	}
	if r.persona == nil || r.agent == nil || r.agent.Pipeline == nil {
		return fmt.Errorf("history fixture: persona agent is not configured")
	}
	provider := r.agent.Pipeline.Brain
	projection, ok := provider.(brain.HistoryProjectionProvider)
	if !ok {
		return fmt.Errorf("history fixture: provider %T does not support ImportHistory", provider)
	}
	if err := projection.ImportHistory(turns); err != nil {
		return fmt.Errorf("history fixture import: %w", err)
	}
	selected := r.persona.selector.Snapshot()
	if err := r.persona.store.SaveSelection(r.persona.session, turns, selected.PersonaID, selected.DisplayName, selected.PromptHash); err != nil {
		return fmt.Errorf("history fixture persistence: %w", err)
	}
	return nil
}
