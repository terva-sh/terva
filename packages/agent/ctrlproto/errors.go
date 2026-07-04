package ctrlproto

import "fmt"

// Sentinel wire errors. Because [*Error] implements error, a [WorkspaceService]
// can return these directly (or a wrapped copy via [Errorf]) and [ServeConn]
// forwards the code to the client unchanged.
var (
	// ErrBusy — a turn is already running on the addressed session.
	ErrBusy = &Error{Code: CodeBusy, Message: "a turn is already running"}
	// ErrNoSession — the addressed session does not exist.
	ErrNoSession = &Error{Code: CodeNoSession, Message: "no such session"}
	// ErrNotFound — a named resource does not exist.
	ErrNotFound = &Error{Code: CodeNotFound, Message: "not found"}
	// ErrUnsupported — the method is not served by this implementation.
	ErrUnsupported = &Error{Code: CodeUnsupported, Message: "not supported"}
)

// Errorf builds a coded wire [*Error].
func Errorf(code, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}
