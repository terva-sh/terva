package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/agent/permissions"
	"terva.sh/terva/packages/agent/talkoot"
)

// rungs are the session's own rungs of the tool-call ladder, for work that no
// person began: the merge rule ahead of the ladder, and the review count
// behind it. The gate of a model's call and the door of an extension's
// host_tool_call take the same list, so the two cannot differ.
func (s *wsSession) rungs() []build.Rung {
	return []build.Rung{build.CallGuard(s.mergeGuard), build.AfterAllow(s.reviewCount)}
}

// reviewDenied starts each refusal of the review count.
const reviewDenied = "tool call denied by the review limit of a routine: "

// reviewCount counts the review requests of a call in a turn with no person
// at its root, against the daily limit of the routine that began the chain
// (decision 0029 rule 17). It refuses the request past the limit. It is a
// build.AfterAllow.
//
// 🔑 It stands behind the ladder. There it runs one time for a call that
// will run, so a call that a hook or the policy refused spends no request,
// and a rewrite of the arguments does not count one call two times.
//
// 🚨 The count is taken before the command runs. A request that then fails
// on the forge still spent one. The other order would let a chain pass its
// limit with requests that are at work at one time.
func (s *wsSession) reviewCount(_ context.Context, tool string, args json.RawMessage) string {
	// The rule gives a first-party tool but bash no count. This return only
	// saves the read of the turn at each such call.
	if tool != "bash" && permissions.IsBuiltin(tool) {
		return ""
	}
	u, lone := s.unattended()
	if !lone {
		return ""
	}
	return reviewRequests(tool, args, func(n int) string {
		b := u.seat.b
		return reviewVerdict(n, chainReviews(b.run, u.root, b.member, n))
	})
}

// reviewRequests reads how many reviews one call of unattended work asks
// for, and has count take that many from the limit. It returns why the call
// is refused, or an empty text. count is not called for a call that asks for
// none. The session's rung and the rung of a swarm child both end here.
func reviewRequests(tool string, args json.RawMessage, count func(n int) string) string {
	if tool != "bash" && permissions.IsBuiltin(tool) {
		return ""
	}
	// Read live, as the merge rule's patterns are. With no pattern the rule
	// counts nothing, and the list of routines says so.
	n, why := permissions.ReviewRule{Commands: config.TalkootRoutines().ReviewCommands}.Count(tool, args)
	if why != "" {
		return reviewDenied + why + ". " +
			"No person began this chain, and each review request of such a chain counts against its routine's limit for one day."
	}
	if n == 0 {
		return ""
	}
	return count(n)
}

// chainReviews counts n review requests of member in the chain with this
// root, against the daily limit of the routine that began the chain.
func chainReviews(run *talkootRun, root, member string, n int) error {
	return run.do(func(rt *talkoot.Router) error {
		// The limit is read at each request, so a change of the record binds
		// the chains that are at work. A chain with no routine names none,
		// and no record reads for an empty id.
		r, err := talkoot.LoadRoutine(run.dir, rt.ChainRoutine(root))
		if err != nil {
			return err
		}
		_, err = rt.CountReviews(root, member, n, r.Fields.ReviewsPerDay)
		return err
	})
}

// reviewVerdict writes what the model reads after a count of n requests that
// ended with err. A nil error is a request that may run.
func reviewVerdict(n int, err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, talkoot.ErrReviewsSpent):
		asks := ""
		if n > 1 {
			asks = fmt.Sprintf(" This command asks for %d reviews.", n)
		}
		return reviewDenied + strings.TrimPrefix(err.Error(), "talkoot: ") + "." + asks + " " +
			"Do not try another route. Say in your last message which pull request waits for a review."
	default:
		// 🚨 A request that terva could not count does not run. The room
		// refused the line, the routine is gone, or the talkoot closed.
		return reviewDenied + "terva could not count this review request (" + err.Error() + "). " +
			"Do not try another route. Say in your last message which pull request waits for a review."
	}
}
