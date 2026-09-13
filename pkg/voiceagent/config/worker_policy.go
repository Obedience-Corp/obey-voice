package config

import (
	"context"
	"errors"
	"path/filepath"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/usage"
)

// WorkerLedgerPath is shared by all personas and sessions in this installation.
func WorkerLedgerPath() string { return filepath.Join(ConfigDir(), "worker-usage.sqlite") }

// WithSavedWorkerPolicy serializes paid admission with Settings saves. The
// callback may perform bounded local ledger I/O, but must not call a provider
// or acquire application state locks. The admitted assignment remains pinned;
// the installation-wide policy always comes from the current file.
func WithSavedWorkerPolicy(ctx context.Context, fn func(*VoiceWorkers) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	release, err := openConfigForWrite(ConfigFile(), "voice_workers")
	if err != nil {
		return err
	}
	defer release()
	_, _, doc, err := readConfigDocument(ConfigFile(), "voice_workers")
	if err != nil {
		return err
	}
	node := mappingValue(doc.Content[0], "voice_workers")
	if node == nil {
		return errors.New("paid workers require saved worker policy")
	}
	if err := validateWorkerYAML(node); err != nil {
		return err
	}
	var saved VoiceWorkers
	if err := node.Decode(&saved); err != nil {
		return err
	}
	if err := saved.Validate(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return fn(&saved)
}

const WorkerPolicyID = "workers"

// WorkerAllowance reads shared accounting on explicit inspection. It does not
// construct a conversation coordinator or invoke a model.
func WorkerAllowance(ctx context.Context) (usage.Snapshot, error) {
	var snapshot usage.Snapshot
	err := WithSavedWorkerPolicy(ctx, func(current *VoiceWorkers) error {
		ledger, err := usage.OpenContext(ctx, WorkerLedgerPath())
		if err != nil {
			return err
		}
		defer ledger.Close()
		p := current.Policy
		if err := ledger.SyncPolicy(ctx, usage.Policy{ID: WorkerPolicyID, Revision: current.Revision, PaidEnabled: p.PaidEnabled, Strict: p.ChargePolicy == "strict", DailyMicroUSD: p.DailyChargeMicroUSD, Timezone: p.Timezone}); err != nil {
			return err
		}
		snapshot, err = ledger.Snapshot(ctx, WorkerPolicyID, time.Now())
		return err
	})
	return snapshot, err
}
