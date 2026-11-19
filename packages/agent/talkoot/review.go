package talkoot

import (
	"errors"
	"fmt"
)

// The review requests of a chain that a routine began (decision 0029 rule
// 17).
//
// A model review runs on the forge, and the daemon cannot see what it costs.
// It can count the requests. So a routine has a limit for one day, and each
// request of a chain that the routine began counts against it: the run's own
// requests, and those of the member that reads the run's report.
//
// 🔑 The count is a line in the room. A count in memory alone would give a
// routine its reviews again at each restart of the daemon.

// ErrReviewsSpent refuses a review request of a chain whose routine has none
// left today.
var ErrReviewsSpent = errors.New("talkoot: the routine has no review request left today")

// ErrNoRoutineChain refuses a count for a chain that no routine began.
var ErrNoRoutineChain = errors.New("talkoot: no routine began this chain")

// ChainRoutine returns the id of the routine that began the chain with this
// root. It is empty for a chain that a person began, and for a root that the
// router does not know.
func (rt *Router) ChainRoutine(root string) string {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if cs := rt.chains[root]; cs != nil {
		return cs.routine
	}
	return ""
}

// RoutineReviews returns how many reviews the chains of a routine asked for
// today.
func (rt *Router) RoutineReviews(routine string) int {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	rt.roll(rt.now())
	return rt.routineReviews[routine]
}

// CountReviews counts n review requests of member in the chain with this
// root against limit, which is the daily limit of the chain's routine. It
// returns the count of the routine's day after the call.
//
// 🚨 The check and the count are one step under the router's lock. Two calls
// at one time cannot both take the last request.
//
// 🚨 A request that would pass the limit counts nothing, and the error wraps
// ErrReviewsSpent. A line that the room refused counts nothing too, and the
// caller refuses the tool call: a request that is not in the room is one that
// a restart forgets.
func (rt *Router) CountReviews(root, member string, n, limit int) (int, error) {
	if n < 1 {
		return 0, fmt.Errorf("talkoot: a count of %d review requests is no count", n)
	}
	rt.mu.Lock()
	defer rt.mu.Unlock()
	cs := rt.chains[root]
	if cs == nil || cs.routine == "" {
		return 0, ErrNoRoutineChain
	}
	now := rt.now()
	rt.roll(now)
	asked := rt.routineReviews[cs.routine]
	if asked+n > limit {
		return asked, fmt.Errorf("%w: its chains asked for %d today, and its limit is %d a day", ErrReviewsSpent, asked, limit)
	}
	if err := rt.room.Append(Line{Type: LineReview, At: now, Member: member, Chain: root, Ref: RoutinePrefix + cs.routine, Count: n}); err != nil {
		return asked, err
	}
	rt.routineReviews[cs.routine] = asked + n
	return asked + n, nil
}
