package browser

import (
	"context"
	"errors"
	"fmt"
	"net"

	"github.com/christopherdavenport/unblink/internal/session"
	"github.com/christopherdavenport/unblink/internal/tokens"
)

// ErrorCode classifies a browser error so the MCP layer (and the calling agent)
// can react programmatically instead of parsing prose: distinguish bad arguments
// from expired sessions from transient network failures.
type ErrorCode string

const (
	ErrBadInput       ErrorCode = "bad_input"       // caller error: fix the arguments
	ErrUnknownSession ErrorCode = "unknown_session" // id never existed (or was closed)
	ErrSessionExpired ErrorCode = "session_expired" // id was evicted; state and credentials are gone
	ErrNoCurrentPage  ErrorCode = "no_current_page" // session exists but has never navigated
	ErrJSRequired     ErrorCode = "js_required"     // needs the server started with --js
	ErrNotConfigured  ErrorCode = "not_configured"  // feature needs server-side configuration
	ErrBlocked        ErrorCode = "blocked"         // SSRF guard denied the target address
	ErrCursorExpired  ErrorCode = "cursor_expired"  // page content changed; restart without a cursor
	ErrTimeout        ErrorCode = "timeout"         // the operation ran out of time
	ErrFetchFailed    ErrorCode = "fetch_failed"    // network-level fetch failure
	ErrInternal       ErrorCode = "internal"        // unexpected failure inside unblink
)

// Error is a classified browser error. Message is agent-facing and actionable;
// Retryable hints whether the same call could succeed if simply retried.
type Error struct {
	Code      ErrorCode
	Retryable bool
	Message   string
	Err       error // wrapped cause; may be nil
}

func (e *Error) Error() string { return e.Message }
func (e *Error) Unwrap() error { return e.Err }

// errf builds a non-retryable classified error.
func errf(code ErrorCode, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// errNoCurrentPage is the shared "session exists but has never navigated" error,
// kept identical across every tool so agents see one consistent failure mode.
func errNoCurrentPage(id string) *Error {
	return errf(ErrNoCurrentPage, "session %q has no current page; navigate it first by calling a page tool with a url", id)
}

// Classify wraps err as a *Error, passing an already-classified error through
// unchanged. Session, cursor, SSRF, and timeout causes map to their codes; any
// other failure is a fetch-level or internal error.
func Classify(err error) *Error {
	if err == nil {
		return nil
	}
	var be *Error
	if errors.As(err, &be) {
		return be
	}

	var expired *session.ExpiredError
	if errors.As(err, &expired) {
		msg := fmt.Sprintf("session %q expired (evicted: %s) and its cookies and page state are gone; re-create it with session(action=new)",
			expired.ID, expired.Reason)
		if expired.HadCredentials {
			msg += " and re-attach its credentials"
		}
		return &Error{Code: ErrSessionExpired, Message: msg, Err: err}
	}
	var notFound *session.NotFoundError
	if errors.As(err, &notFound) {
		return &Error{Code: ErrUnknownSession, Err: err,
			Message: fmt.Sprintf("unknown session %q; create it with session(action=new) or navigate it with a page tool", notFound.ID)}
	}
	var exists *session.ExistsError
	if errors.As(err, &exists) {
		return &Error{Code: ErrBadInput, Message: exists.Error(), Err: err}
	}

	if errors.Is(err, tokens.ErrStaleCursor) {
		return &Error{Code: ErrCursorExpired, Err: err,
			Message: "the cursor is stale: the page content changed since it was issued; call read again without a cursor to restart from page 1"}
	}
	if errors.Is(err, errBlockedAddr) {
		return &Error{Code: ErrBlocked, Err: err,
			Message: "the target resolves to a private/loopback/metadata address, which unblink blocks by default (SSRF guard); use --allow-private only for trusted internal targets"}
	}
	if errors.Is(err, ErrSearchDisabled) {
		return &Error{Code: ErrNotConfigured, Message: ErrSearchDisabled.Error(), Err: err}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return &Error{Code: ErrTimeout, Retryable: true, Message: err.Error(), Err: err}
	}
	var nerr net.Error
	if errors.As(err, &nerr) {
		return &Error{Code: ErrFetchFailed, Retryable: nerr.Timeout(), Message: err.Error(), Err: err}
	}
	return &Error{Code: ErrInternal, Message: err.Error(), Err: err}
}
