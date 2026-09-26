package talkoot

import "strings"

// Pause kinds. Pauses stack: one scope can be paused by a person and by a cap
// at once. Each keeps its own reason, and a status shows every one, so a
// person sees what still holds a member after a resume.
const (
	pausePerson = "person" // a person paused it
	pauseSpend  = "spend"  // the member's spend cap tripped
	pauseTurns  = "turns"  // the member's turn cap tripped
	pauseTeam   = "team"   // the talkoot's spend cap tripped
	pauseCost   = "cost"   // a driver reported a cost that cannot be counted
	pauseGuard  = "guard"  // the hop limit tripped
	pauseRoom   = "room"   // the room could not record something, or cannot be trusted
)

// pauseKinds is the order a status lists reasons in.
//
// Each cap is a kind of its own, so a member over its spend and its turns
// shows both, and a resume that lifts one still names the other.
var pauseKinds = []string{pausePerson, pauseSpend, pauseTurns, pauseTeam, pauseCost, pauseGuard, pauseRoom}

// talkootScope is the scope of a pause on every member at once.
const talkootScope = "talkoot"

func memberScope(id string) string  { return "member:" + id }
func chainScope(root string) string { return "chain:" + root }

// scopeOf names a pause's scope the way the room records it: a member, a
// chain, or, with neither, the whole talkoot.
func scopeOf(member, chain string) string {
	switch {
	case member != "":
		return memberScope(member)
	case chain != "":
		return chainScope(chain)
	}
	return talkootScope
}

// kindOfGuard maps a guard line back to the pause kind that wrote it.
func kindOfGuard(guard string) string {
	switch guard {
	case GuardPerson:
		return pausePerson
	case GuardSpend:
		return pauseSpend
	case GuardTurns:
		return pauseTurns
	case GuardTeamSpend:
		return pauseTeam
	case GuardCost:
		return pauseCost
	case GuardRoom:
		return pauseRoom
	}
	return pauseGuard
}

// pauses maps a scope to its pauses by kind.
type pauses map[string]map[string]string

func (p pauses) set(scope, kind, reason string) {
	if p[scope] == nil {
		p[scope] = map[string]string{}
	}
	p[scope][kind] = reason
}

func (p pauses) has(scope, kind string) bool {
	_, ok := p[scope][kind]
	return ok
}

// reason joins every pause on a scope, or returns "" when there is none.
func (p pauses) reason(scope string) string {
	var out []string
	for _, k := range pauseKinds {
		if r, ok := p[scope][k]; ok {
			out = append(out, r)
		}
	}
	return strings.Join(out, "; ")
}

func (p pauses) drop(scope, kind string) {
	delete(p[scope], kind)
	if len(p[scope]) == 0 {
		delete(p, scope)
	}
}

// clear removes every pause on a scope.
func (p pauses) clear(scope string) { delete(p, scope) }
