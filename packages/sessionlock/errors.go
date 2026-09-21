package sessionlock

import (
	"errors"
	"fmt"
)

// ErrLocked is what every refusal from this package unwraps to, so a caller
// tests one sentinel with errors.Is and does not have to know about Cause.
var ErrLocked = errors.New("this session is open in another terva")

// Cause says which witness refused, because the three want different answers
// from the person reading them. A live handle means stop that process. An
// explicit claim means release it or wait for it to lapse. The unflocked case
// means the filesystem does not support locking and the answer is weaker than
// it looks.
type Cause string

const (
	// CauseLiveHandle: the flock is held, so a process has this session open.
	CauseLiveHandle Cause = "a live write handle"
	// CauseLiveHandleUnflocked: the flock was free, but the record describes a
	// holder that is still alive by both witnesses. On a filesystem that
	// honours locks this cannot happen.
	CauseLiveHandleUnflocked Cause = "a live write handle on a filesystem that does not honour file locks"
	// CauseExplicitClaim: somebody claimed this session on purpose.
	CauseExplicitClaim Cause = "an explicit claim"
)

// BusyError reports a refusal along with whatever the record said, so a caller
// can render the holder and the expiry rather than a bare "locked".
type BusyError struct {
	Path     string
	Cause    Cause
	Claim    Claim
	HasClaim bool
}

func (e *BusyError) Error() string {
	if e.HasClaim {
		return fmt.Sprintf("%s: %s", ErrLocked.Error(), e.Claim.Describe())
	}
	return fmt.Sprintf("%s: %s, and it left no record of why", ErrLocked.Error(), e.Cause)
}

// Unwrap lets errors.Is(err, ErrLocked) answer for every cause.
func (e *BusyError) Unwrap() error { return ErrLocked }
