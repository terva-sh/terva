package ctrlproto

import (
	"context"
	"time"
)

// The daemon's timers, read-only. A timer belongs to an owner inside the
// daemon, such as a talkoot's wakes, and only that owner sets or cancels one.
// This verb exists so that a person can see what the daemon will do by itself,
// and when.

// TimersController is optional. A terminal has no timer service, and a replay
// carrier has no daemon, so each answers CodeUnsupported.
type TimersController interface {
	// TimerList returns every timer the daemon holds, the soonest first.
	TimerList(ctx context.Context) (TimersResult, error)
}

// TimersResult is the daemon's timers. Problem says why the service does not
// run or cannot fire, such as a timers file it cannot write, and it is empty
// while the service is in order.
type TimersResult struct {
	Timers  []TimerInfo `json:"timers"`
	Problem string      `json:"problem,omitempty"`
}

// TimerInfo is one timer. Schedule is its text form, such as "every 15m" or
// "weekdays 07:30". Next is the next window that has not fired. Problem says
// why this timer waits, such as an owner that this daemon does not run.
type TimerInfo struct {
	Owner     string    `json:"owner"`
	ID        string    `json:"id"`
	Label     string    `json:"label,omitempty"`
	Schedule  string    `json:"schedule"`
	Recurs    bool      `json:"recurs,omitempty"`
	Next      time.Time `json:"next,omitzero"`
	LastFired time.Time `json:"last_fired,omitzero"`
	Problem   string    `json:"problem,omitempty"`
}
