package pipeline

import "errors"

// AdmissionError marks a turn that failed at the admission boundary before
// model generation started.
type AdmissionError struct {
	Err error
}

func (e *AdmissionError) Error() string {
	if e == nil || e.Err == nil {
		return "admit response"
	}
	return "admit response: " + e.Err.Error()
}

func (e *AdmissionError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func wrapAdmissionError(err error) error {
	if err == nil {
		return nil
	}
	var existing *AdmissionError
	if errors.As(err, &existing) {
		return err
	}
	return &AdmissionError{Err: err}
}
