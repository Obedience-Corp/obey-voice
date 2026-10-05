package personaruntime

import (
	"context"
	"errors"

	"github.com/Obedience-Corp/obey-voice/internal/persona"
	"github.com/Obedience-Corp/obey-voice/internal/summon"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/admission"
)

func classifyAdmissionFailure(ctx context.Context, operation string, err error) error {
	if err == nil {
		return nil
	}
	var existing *admission.Failure
	if errors.As(err, &existing) {
		return err
	}
	if ctx != nil && ctx.Err() != nil {
		return admission.WrapFailure(admission.FailureParentCancellation, operation, err)
	}
	var classifierFailure *admission.ClassifierFailure
	if errors.As(err, &classifierFailure) {
		switch classifierFailure.Kind {
		case admission.ClassifierUnavailable:
			return admission.WrapFailure(admission.FailureClassifierUnavailable, operation, err)
		case admission.ClassifierLocalTimeout:
			return admission.WrapFailure(admission.FailureClassifierLocalTimeout, operation, err)
		case admission.ClassifierInvalidOutput:
			return admission.WrapFailure(admission.FailureClassifierInvalidOutput, operation, err)
		}
	}
	return err
}

func wrapIdentityRefusalFailure(ctx context.Context, operation string, err error) error {
	if err == nil {
		return nil
	}
	classified := classifyAdmissionFailure(ctx, operation, err)
	if classified != err {
		return classified
	}
	return admission.WrapFailure(admission.FailureIdentityRefusal, operation, err)
}

func wrapInvalidClassifierOutput(operation string, err error) error {
	if err == nil {
		return nil
	}
	return admission.WrapFailure(admission.FailureClassifierInvalidOutput, operation, err)
}

func isIdentityRefusal(err error) bool {
	var unknown *summon.UnknownTargetError
	if errors.As(err, &unknown) {
		return true
	}
	var ambiguous *summon.AmbiguousTargetError
	if errors.As(err, &ambiguous) {
		return true
	}
	var profileErr *persona.ProfileLoadError
	if errors.As(err, &profileErr) {
		return true
	}
	return errors.Is(err, persona.ErrInactive)
}
