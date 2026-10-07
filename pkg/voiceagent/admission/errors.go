package admission

import "errors"

// ClassifierFailureKind is the classifier-only failure class emitted by the
// model-facing boundary.
type ClassifierFailureKind string

const (
	ClassifierUnavailable   ClassifierFailureKind = "classifier_unavailable"
	ClassifierLocalTimeout  ClassifierFailureKind = "classifier_local_timeout"
	ClassifierInvalidOutput ClassifierFailureKind = "classifier_invalid_output"
)

// ClassifierFailure preserves the original error while attaching classifier
// diagnostics for host-level routing failures.
type ClassifierFailure struct {
	Kind ClassifierFailureKind
	Err  error
}

func (e *ClassifierFailure) Error() string {
	if e == nil || e.Err == nil {
		return ""
	}
	return e.Err.Error()
}

func (e *ClassifierFailure) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func WrapClassifierFailure(kind ClassifierFailureKind, err error) error {
	if err == nil {
		return nil
	}
	var existing *ClassifierFailure
	if errors.As(err, &existing) && existing.Kind == kind {
		return err
	}
	return &ClassifierFailure{Kind: kind, Err: err}
}

func ClassifierFailureKindOf(err error) (ClassifierFailureKind, bool) {
	var failure *ClassifierFailure
	if !errors.As(err, &failure) {
		return "", false
	}
	return failure.Kind, true
}

// FailureCategory is the host-facing route-admission class used for policy and
// UI projection.
type FailureCategory string

const (
	FailureClassifierUnavailable   FailureCategory = "classifier_unavailable"
	FailureClassifierLocalTimeout  FailureCategory = "classifier_local_timeout"
	FailureClassifierInvalidOutput FailureCategory = "classifier_invalid_output"
	FailureIdentityRefusal         FailureCategory = "identity_refusal"
	FailureParentCancellation      FailureCategory = "parent_cancellation"
)

// Failure wraps an admission error with a host-consumable category and
// boundary operation while preserving the underlying cause chain.
type Failure struct {
	Category  FailureCategory
	Operation string
	Err       error
}

func (e *Failure) Error() string {
	if e == nil || e.Err == nil {
		return ""
	}
	return e.Err.Error()
}

func (e *Failure) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func WrapFailure(category FailureCategory, operation string, err error) error {
	if err == nil {
		return nil
	}
	var existing *Failure
	if errors.As(err, &existing) && existing.Category == category && existing.Operation == operation {
		return err
	}
	return &Failure{Category: category, Operation: operation, Err: err}
}

func FailureCategoryOf(err error) (FailureCategory, bool) {
	var failure *Failure
	if !errors.As(err, &failure) {
		return "", false
	}
	return failure.Category, true
}
