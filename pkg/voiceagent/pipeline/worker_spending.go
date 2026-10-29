package pipeline

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/chatmedia"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/job"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/usage"
)

const workerPolicyID = config.WorkerPolicyID

// paidWorkerRun admits one opaque CLI operation. No current paid adapter has a
// certified request/charge bound, so only explicit advisory uncapped use can
// reach this path. The record remains uncertain even when a CLI reports cost.
func (a *Agency) paidWorkerRun(ctx context.Context, brief string, worker brain.Provider, cfg *config.Config, plan *config.WorkerPlan, requestID string) (string, error) {
	if plan.Assignment.AccountRef == nil || !plan.AllowUncapped || plan.Policy.ChargePolicy != "advisory" || !plan.Policy.PaidEnabled {
		return "", errors.New("paid worker lacks explicit advisory uncapped consent")
	}
	// Reject unsupported/missing media before recording a paid dispatch.
	if err := validateProviderImages(ctx, worker, chatmedia.Input(ctx)); err != nil {
		return "", err
	}
	a.emitChargeState(job.IDFromContext(ctx), requestID, "checking")
	var ledger *usage.Ledger
	var reservation usage.Reservation
	defer func() {
		if ledger != nil {
			_ = ledger.Close()
		}
	}()
	admission, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	err := config.WithSavedWorkerPolicy(admission, func(current *config.VoiceWorkers) error {
		if !current.Policy.PaidEnabled || current.Policy.ChargePolicy != "advisory" || current.Mode != config.WorkerModeTiered {
			return fmt.Errorf("%w: current policy blocks uncapped paid workers", ErrWorkerPolicyChanged)
		}
		var err error
		ledger, err = usage.OpenContext(admission, config.WorkerLedgerPath())
		if err != nil {
			return err
		}
		p := current.Policy
		if err := ledger.SyncPolicy(admission, usage.Policy{ID: workerPolicyID, Revision: current.Revision, PaidEnabled: p.PaidEnabled, Strict: p.ChargePolicy == "strict", DailyMicroUSD: p.DailyChargeMicroUSD, Timezone: p.Timezone}); err != nil {
			return err
		}
		reservation, err = ledger.Reserve(admission, usage.Request{ID: requestID, Account: plan.Assignment.Provider + ":" + *plan.Assignment.AccountRef, PolicyID: workerPolicyID, PolicyRevision: current.Revision, AllowUncapped: true, Now: time.Now()})
		if err != nil {
			return err
		}
		if !reservation.Created {
			return errors.New("worker request already admitted; replay will not dispatch")
		}
		// This commit is the send boundary. A crash or cancellation after it retains
		// an uncertain obligation, including a crash before the actual helper call.
		return ledger.MarkDispatched(admission, requestID, current.Revision)
	})
	if err != nil {
		return "", fmt.Errorf("checking worker allowance: %w", err)
	}
	a.emitChargeState(job.IDFromContext(ctx), requestID, "dispatched")
	result, runErr := a.workerRunWithBudget(brief, worker, cfg, nil)(ctx)
	cleanup, finish := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer finish()
	settleErr := ledger.MarkUncertain(cleanup, requestID)
	if settleErr == nil {
		a.emitChargeState(job.IDFromContext(ctx), requestID, "uncertain")
	}
	if settleErr != nil {
		settleErr = fmt.Errorf("retain uncertain worker charge: %w", settleErr)
	}
	return result, errors.Join(runErr, settleErr)
}
func (a *Agency) emitChargeState(jobID, requestID, state string) {
	_, key, ok := a.callbackKey(jobID)
	if !ok || a.pipeline == nil {
		return
	}
	a.pipeline.emit(events.WorkerChargeState{Scope: key.Scope, JobID: jobID, RequestID: requestID, State: state, ChargeCapAvailable: false})
}
