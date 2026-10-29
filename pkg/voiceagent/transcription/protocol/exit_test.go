package protocol

import (
	"errors"
	"slices"
	"testing"
)

// TestCodesAndExitsAgree is the guard the docs task relies on: the protocol
// reference renders Codes(), so a code without an exit status (or an exit
// status without a code) is caught here rather than in the published table.
func TestCodesAndExitsAgree(t *testing.T) {
	list := Codes()
	if len(list) != len(codeExit) {
		t.Fatalf("Codes() has %d entries, codeExit has %d", len(list), len(codeExit))
	}
	for _, code := range list {
		if !KnownCode(code) {
			t.Errorf("Codes() lists %q, which has no exit status", code)
		}
	}
	for code := range codeExit {
		if !slices.Contains(list, code) {
			t.Errorf("codeExit maps %q, which Codes() does not list", code)
		}
	}
}

func TestExitFor(t *testing.T) {
	cases := map[string]int{
		CodeInvalidRequest:        ExitInvalid,
		CodeUnsupportedProtocol:   ExitInvalid,
		CodeUnsupportedCapability: ExitInvalid,
		CodeInputUnreadable:       ExitInvalid,
		CodeInputFormatMismatch:   ExitInvalid,
		CodeProviderUnavailable:   ExitUnavailable,
		CodeModelMissing:          ExitUnavailable,
		CodeDecodeFailed:          ExitRuntime,
		CodeInternal:              ExitRuntime,
		"code_from_a_later_build": ExitRuntime,
	}
	for code, want := range cases {
		if got := ExitFor(code); got != want {
			t.Errorf("ExitFor(%q) = %d, want %d", code, got, want)
		}
	}
}

func TestExitForError(t *testing.T) {
	var typedNil *Error
	if got := ExitForError(nil); got != ExitOK {
		t.Errorf("ExitForError(nil) = %d", got)
	}
	if got := ExitForError(typedNil); got != ExitRuntime {
		t.Errorf("ExitForError(typed nil) = %d", got)
	}
	if got := typedNil.Error(); got != "protocol: nil error" {
		t.Errorf("typed nil Error() = %q", got)
	}
	if got := ExitForError(Errorf(CodeModelMissing, false, "x")); got != ExitUnavailable {
		t.Errorf("ExitForError(model_missing) = %d", got)
	}
	if got := ExitForError(errors.New("boom")); got != ExitRuntime {
		t.Errorf("ExitForError(plain) = %d", got)
	}
}

// TestEveryRequestRejectionExitsTwo ties the validator to the exit table: a
// caller that sent a bad request must never see 3 or 4, whatever the code.
func TestEveryRequestRejectionExitsTwo(t *testing.T) {
	req := validRequest()
	req.Timing = TimingWord
	err := req.Validate()
	var coded *Error
	if !errors.As(err, &coded) {
		t.Fatalf("Validate = %v", err)
	}
	if ExitFor(coded.Code) != ExitInvalid {
		t.Fatalf("code %s exits %d, want %d", coded.Code, ExitFor(coded.Code), ExitInvalid)
	}
}
