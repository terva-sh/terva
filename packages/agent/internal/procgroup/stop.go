package procgroup

import (
	"os"
	"time"
)

// pollInterval is how often a grace in Stop checks whether the group is gone.
const pollInterval = 25 * time.Millisecond

// Stop ends the subprocess p and the process group it leads. Call it after you
// asked p to exit, for example by closing its stdin. reaped must close once
// Wait has returned for p, and another goroutine must own that Wait.
//
// Stop waits up to grace for p to be gone (see Gone). If it is not, Stop asks
// the group to stop with Terminate and waits up to termGrace. If p is still not
// gone, Stop kills the group, calls release, and waits for reaped. release must
// free any pipe that a descendant outside the group can still hold, so that the
// goroutine that owns Wait can finish. Stop returns as soon as p is gone.
//
// Stop signals only straight after a check that found the leader unreaped or a
// member left. Either one holds the group id, so no other group can have taken
// it, short of a race of microseconds.
//
// On Windows gone means the leader is reaped, and Kill ends the leader alone.
func Stop(p *os.Process, reaped <-chan struct{}, grace, termGrace time.Duration, release func()) {
	if p == nil {
		return
	}
	if awaitGone(p, reaped, grace) {
		return
	}
	Terminate(p)
	if awaitGone(p, reaped, termGrace) {
		return
	}
	Kill(p)
	if release != nil {
		release()
	}
	<-reaped
}

// Gone reports whether p is gone: reaped is closed, and the group p led has no
// member left.
//
// A reaped leader alone proves little. A member that redirected its stdout
// never held the pipe, so stdout reaches EOF and the leader is reaped while
// that member still runs. Gone therefore asks MembersRemain once the leader is
// reaped, and never before, because MembersRemain asks by number.
//
// Windows cannot ask about members, so there Gone means the leader is reaped.
func Gone(p *os.Process, reaped <-chan struct{}) bool {
	select {
	case <-reaped:
		return !MembersRemain(p)
	default:
		return false
	}
}

// awaitGone waits up to d for p to be gone, and reports whether it is.
func awaitGone(p *os.Process, reaped <-chan struct{}, d time.Duration) bool {
	deadline := time.NewTimer(d)
	defer deadline.Stop()
	poll := time.NewTicker(pollInterval)
	defer poll.Stop()
	wait := reaped
	for {
		select {
		case <-wait:
			wait = nil // closed: from now on the ticker drives the check
		case <-poll.C:
		case <-deadline.C:
			return Gone(p, reaped)
		}
		if Gone(p, reaped) {
			return true
		}
	}
}
