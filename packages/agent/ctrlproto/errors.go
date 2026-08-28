package ctrlproto

import (
	"fmt"

	"terva.sh/terva/packages/i18n"
)

// Sentinel wire errors. Because [*Error] implements error, a [WorkspaceService]
// can return these directly (or a wrapped copy via [Errorf]) and [ServeConn]
// forwards the code to the client unchanged. The messages are declared at
// init so they carry [i18n.M] marks; serveState.fail translates them (on a
// copy) at emission — the Code stays a stable machine string either way.
var (
	// ErrBusy — a turn is already running on the addressed session.
	ErrBusy = &Error{Code: CodeBusy, Message: i18n.M("a turn is already running")}
	// ErrNoSession — the addressed session does not exist.
	ErrNoSession = &Error{Code: CodeNoSession, Message: i18n.M("no such session")}
	// ErrNotFound — a named resource does not exist.
	ErrNotFound = &Error{Code: CodeNotFound, Message: i18n.M("not found")}
	// ErrUnsupported — the method is not served by this implementation.
	ErrUnsupported = &Error{Code: CodeUnsupported, Message: i18n.M("not supported")}
)

// Errorf builds a coded wire [*Error].
func Errorf(code, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// Wrap builds a coded wire [*Error] that also RETAINS cause, so an in-process
// caller can errors.As past it to the typed error underneath.
//
// Use it where the caller needs more than the code — the credential failure
// that opens the TUI's login dialog wants the provider name off
// build.ExpiredLoginError, and reading it out of the formatted sentence would
// couple the dialog to that wording. Over the wire this is [Errorf]: the cause
// does not serialize, and the code is what a remote client gets.
func Wrap(code string, cause error) *Error {
	if cause == nil {
		return &Error{Code: code}
	}
	return &Error{Code: code, Message: cause.Error(), cause: cause}
}
