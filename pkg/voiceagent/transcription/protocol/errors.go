// Package protocol implements the obey-transcript/v1 wire contract: the
// request document, the JSONL event stream, and the process exit statuses.
//
// It is pure Go with no dependency outside the standard library, so any tool
// can speak the protocol without the native speech stack.
package protocol

import "fmt"

// Stable failure codes. A client branches on these strings, so each one is
// part of the wire contract: add a code in a new protocol version, never
// rename or repurpose one.
const (
	CodeInvalidRequest        = "invalid_request"
	CodeUnsupportedProtocol   = "unsupported_protocol"
	CodeUnsupportedCapability = "unsupported_capability"
	CodeInputUnreadable       = "input_unreadable"
	CodeInputFormatMismatch   = "input_format_mismatch"
	CodeProviderUnavailable   = "provider_unavailable"
	CodeModelMissing          = "model_missing"
	CodeDecodeFailed          = "decode_failed"
	CodeInternal              = "internal"
)

// Error is a failure carrying one of the stable codes. The command turns it
// into a failed event and an exit status, so no caller has to parse a message.
type Error struct {
	Code      string
	Retryable bool
	Err       error
}

func (e *Error) Error() string {
	if e == nil {
		return "protocol: nil error"
	}
	if e.Err == nil {
		return e.Code
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Err)
}

func (e *Error) Unwrap() error { return e.Err }

// Errorf builds an Error with a formatted message.
func Errorf(code string, retryable bool, format string, args ...any) *Error {
	return &Error{Code: code, Retryable: retryable, Err: fmt.Errorf(format, args...)}
}
