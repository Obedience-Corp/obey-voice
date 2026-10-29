package protocol

import "errors"

// Process exit statuses. A client requires both a completed event and ExitOK
// before it treats a transcript as finished.
const (
	ExitOK          = 0
	ExitInvalid     = 2
	ExitUnavailable = 3
	ExitRuntime     = 4
	ExitCancelled   = 130
)

// codeExit maps every stable failure code to its exit status.
var codeExit = map[string]int{
	CodeInvalidRequest:        ExitInvalid,
	CodeUnsupportedProtocol:   ExitInvalid,
	CodeUnsupportedCapability: ExitInvalid,
	CodeInputUnreadable:       ExitInvalid,
	CodeInputFormatMismatch:   ExitInvalid,
	CodeProviderUnavailable:   ExitUnavailable,
	CodeModelMissing:          ExitUnavailable,
	CodeDecodeFailed:          ExitRuntime,
	CodeInternal:              ExitRuntime,
}

// codes is the stable failure-code list in documentation order: request
// problems, then input problems, then installation, then runtime.
var codes = []string{
	CodeInvalidRequest,
	CodeUnsupportedProtocol,
	CodeUnsupportedCapability,
	CodeInputUnreadable,
	CodeInputFormatMismatch,
	CodeProviderUnavailable,
	CodeModelMissing,
	CodeDecodeFailed,
	CodeInternal,
}

// Codes lists the stable failure codes in documentation order. The protocol
// reference renders this slice, so the docs cannot drift from the code.
func Codes() []string { return append([]string(nil), codes...) }

// KnownCode reports whether code is one of the stable failure codes.
func KnownCode(code string) bool {
	_, ok := codeExit[code]
	return ok
}

// ExitFor returns the exit status for a failure code. An unrecognized code is
// a runtime failure: a client that cannot branch on the code still learns the
// job did not complete.
func ExitFor(code string) int {
	if status, ok := codeExit[code]; ok {
		return status
	}
	return ExitRuntime
}

// ExitForError returns the exit status for err: the mapped status of its
// *Error code, and ExitRuntime for anything else.
func ExitForError(err error) int {
	if err == nil {
		return ExitOK
	}
	var coded *Error
	if errors.As(err, &coded) && coded != nil {
		return ExitFor(coded.Code)
	}
	return ExitRuntime
}
