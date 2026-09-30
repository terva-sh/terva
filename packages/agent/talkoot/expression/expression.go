// Package expression turns what happens to a Talkoot member into an
// expression, a held pose for its face, and into beats, which play once.
//
// docs/proposals/talkoot-members.md, "The engine decides when", is the design.
// The daemon reaches an engine through [Engine], so another engine can replace
// the table this package ships. Every engine keeps four rules:
//
//   - It never invents a signal. Every expression and every beat carries a
//     cause: the signals that produced it.
//   - It picks from the closed sets of poses and beats below.
//   - Its randomness chooses among reactions to one real signal, and never
//     starts a reaction. A seed drives it, and the trace records the seed.
//   - It is repeatable. The same room lines and seed give the same
//     expressions at the same time, so a replay on start rebuilds each
//     member's mood.
//
// An engine reads only room lines, never the router's calls. The live engine
// and a replay then read one order.
package expression

import (
	"time"

	"terva.sh/terva/packages/agent/talkoot"
)

// Held poses. The renderer owns how each one looks.
const (
	PoseOpen         = "open"
	PoseFocused      = "focused"
	PoseLookingUp    = "looking-up"
	PoseHalfLidded   = "half-lidded"
	PoseClosedSquint = "closed-squint"
	PoseWorried      = "worried"
	PoseFrustrated   = "frustrated"
	PoseSkeptical    = "skeptical"
	PoseClosed       = "closed"
)

// Beats, which play once and return to the held pose.
const (
	BeatHappy     = "happy"
	BeatGlance    = "glance"
	BeatSlowBlink = "slow-blink"
	BeatFastBlink = "fast-blink"
)

// Poses lists every held pose, and Beats every beat.
var (
	Poses = []string{PoseOpen, PoseFocused, PoseLookingUp, PoseHalfLidded, PoseClosedSquint, PoseWorried, PoseFrustrated, PoseSkeptical, PoseClosed}
	Beats = []string{BeatHappy, BeatGlance, BeatSlowBlink, BeatFastBlink}
)

// Expression is a member's held pose. An empty Pose draws the default pose
// for the member's presence. Intensity 0 is the base pose, and 1 its strong
// form.
type Expression struct {
	Pose      string
	Intensity int
	Cause     string
}

// Beat is one play of a beat. Toward names a member, on a glance.
type Beat struct {
	Member string
	Beat   string
	Toward string
	Cause  string
	At     time.Time
}

// Entry is one record in a member's trace: a signal the engine received, and
// what it chose. Pose and Beat are empty when the signal changed nothing.
type Entry struct {
	At        time.Time
	Signal    string
	Pose      string
	Intensity int
	Beat      string
	Toward    string
	Cause     string
}

// Engine is what the daemon reaches an engine through. Its methods may be
// called from several goroutines.
type Engine interface {
	// Observe takes the next room line, in the room's order, and returns the
	// beats it plays. On start the daemon replays every line through it and
	// drops the beats, then calls Replayed.
	Observe(l talkoot.Line) []Beat
	// Replayed ends the replay. What only a running daemon holds, such as an
	// open card, did not survive the stop.
	Replayed()
	// Expression is member's expression at now.
	Expression(member string, now time.Time) Expression
	// Next is the first time after now at which an expression changes with
	// time alone, or the zero time when none will.
	Next(now time.Time) time.Time
	// Advance records in the trace what time alone changed up to now. Each
	// entry carries the moment of its change, not now, so a replay, which
	// advances to each line's time, records the same entries.
	Advance(now time.Time)
	// Trace returns member's trace, oldest first.
	Trace(member string) []Entry
	// Seed is the seed that drives the engine's choices.
	Seed() uint64
}
