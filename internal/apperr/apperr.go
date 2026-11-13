// Package apperr defines seox's named error codes, shared by the JSON output and
// the exit codes. Agents switch on these codes, so never rename one.
package apperr

import (
	"context"
	"errors"
	"fmt"
	"net"
)

const (
	InvalidInput   = "invalid_input"
	NotFound       = "not_found"
	NotImplemented = "not_implemented"
	QuotaExceeded  = "quota_exceeded" // a source's daily limit is used up
	NeedsAPIKey    = "needs_api_key"  // the command only works with a key (seox key set <name>)
	LoginRequired  = "login_required" // the command needs `seox login` (Google OAuth)
	Forbidden      = "forbidden"      // the site or API refused (robots.txt, 403, not your property)
	SourceError    = "source_error"   // a source answered with something seox couldn't read
	ToolMissing    = "tool_missing"   // something seox needs (SearXNG) is not installed
	NetworkError   = "network_error"
	Timeout        = "timeout" // the command's time limit ran out, or Ctrl-C cancelled it
	Internal       = "internal_error"
)

// ExitCodes groups codes by exit status, so scripts can branch without parsing JSON.
var ExitCodes = map[int][]string{
	1: {Internal, SourceError, NotImplemented},
	2: {InvalidInput},
	3: {NotFound},
	4: {LoginRequired, NeedsAPIKey, Forbidden},
	5: {QuotaExceeded},
	7: {NetworkError, Timeout},
	8: {ToolMissing},
}

// ExitCode is the process exit status for a code.
func ExitCode(code string) int {
	for n, codes := range ExitCodes {
		for _, c := range codes {
			if c == code {
				return n
			}
		}
	}
	return 1
}

// Error is a coded error. Detail is optional structured data (e.g. what was tried).
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Detail  any    `json:"detail,omitempty"`
	Err     error  `json:"-"`
}

func (e *Error) Error() string { return e.Message }
func (e *Error) Unwrap() error { return e.Err }

// New makes a coded error.
func New(code, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// Wrap makes a coded error that keeps the cause.
func Wrap(code string, err error, format string, args ...any) *Error {
	msg := fmt.Sprintf(format, args...)
	if err != nil {
		msg += ": " + err.Error()
	}
	return &Error{Code: code, Message: msg, Err: err}
}

// From turns any error into a coded one.
func From(err error) *Error {
	if err == nil {
		return nil
	}
	var e *Error
	if errors.As(err, &e) {
		return e
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return Wrap(Timeout, err, "timed out")
	}
	if errors.Is(err, context.Canceled) {
		return Wrap(Timeout, err, "cancelled")
	}
	var ne net.Error
	if errors.As(err, &ne) {
		if ne.Timeout() {
			return Wrap(Timeout, err, "timed out")
		}
		return Wrap(NetworkError, err, "network error")
	}
	return &Error{Code: Internal, Message: err.Error(), Err: err}
}
