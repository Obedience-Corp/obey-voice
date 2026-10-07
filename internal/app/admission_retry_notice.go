package app

import (
	"fmt"
	"strings"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/admission"
)

// AdmissionRetryNotice describes manual retry guidance for an admission failure.
// It is display-only guidance; turns remain failed and are never replayed.
type AdmissionRetryNotice struct {
	Category admission.FailureCategory
	Message  string
}

// BuildAdmissionRetryNotice maps wrapped admission failures to user-facing
// manual retry guidance. Non-admission failures and parent cancellation return
// ok=false.
func BuildAdmissionRetryNotice(err error, originalInput string) (notice AdmissionRetryNotice, ok bool) {
	category, ok := admissionFailureCategory(err)
	if !ok || category == admission.FailureParentCancellation {
		return AdmissionRetryNotice{}, false
	}
	quotedInput := quotedOriginalInput(originalInput)
	switch category {
	case admission.FailureClassifierUnavailable:
		return AdmissionRetryNotice{
			Category: category,
			Message: "Persona routing is temporarily unavailable (classifier unavailable). " +
				"Resubmit this request manually, or use exact persona control first (for example: \"switch to Santa\"), then ask your question again." +
				quotedInput,
		}, true
	case admission.FailureClassifierLocalTimeout:
		return AdmissionRetryNotice{
			Category: category,
			Message: "Persona routing timed out (classifier timeout). " +
				"Resubmit this request manually, or use exact persona control first (for example: \"switch to Santa\"), then ask your question again." +
				quotedInput,
		}, true
	case admission.FailureClassifierInvalidOutput:
		return AdmissionRetryNotice{
			Category: category,
			Message: "Persona routing returned an invalid decision (classifier invalid output). " +
				"Resubmit this request manually, or use exact persona control first (for example: \"switch to Santa\"), then ask your question again." +
				quotedInput,
		}, true
	case admission.FailureIdentityRefusal:
		return AdmissionRetryNotice{
			Category: category,
			Message: "Persona routing refused that identity selection (identity refusal). " +
				"Use exact persona control first (for example: \"switch to Santa\"), then ask your question again." +
				quotedInput,
		}, true
	default:
		return AdmissionRetryNotice{
			Category: category,
			Message:  "The request was not admitted. Please retry with fresh input." + quotedInput,
		}, true
	}
}

func quotedOriginalInput(input string) string {
	input = strings.TrimSpace(input)
	if input == "" {
		return ""
	}
	return fmt.Sprintf(" Original input: %q.", input)
}
