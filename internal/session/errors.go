package session

import "fmt"

// NotFoundError reports a session id that has never existed (or was explicitly
// closed, which frees the id for reuse).
type NotFoundError struct{ ID string }

func (e *NotFoundError) Error() string { return fmt.Sprintf("unknown session %q", e.ID) }

// ExpiredError reports a session id that existed but was evicted — either idle
// past the TTL ("idle") or displaced at capacity ("capacity"). Its cookies, page
// state, and credentials are gone; the id must be re-created deliberately.
type ExpiredError struct {
	ID             string
	Reason         string // "idle" | "capacity"
	HadCredentials bool
}

func (e *ExpiredError) Error() string {
	return fmt.Sprintf("session %q expired (evicted: %s)", e.ID, e.Reason)
}

// ExistsError reports an attempt to re-create a live session with credentials.
// Credentials are fixed at creation; the session must be closed first.
type ExistsError struct{ ID string }

func (e *ExistsError) Error() string {
	return fmt.Sprintf("session %q already exists; close it before re-creating it with new credentials", e.ID)
}
