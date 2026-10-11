// Package procgroup runs a subprocess as the leader of its own process group
// and signals that group. A teardown then reaches the background children the
// subprocess started, and not only the subprocess itself.
//
// Isolate runs before cmd.Start. Terminate and Kill address the group that the
// started process leads. On Unix that is a POSIX process group. On Windows it
// is a console process group: Terminate reaches the whole group, and Kill ends
// the leader alone.
//
// MembersRemain asks, after Wait reaped the leader, whether its group still
// has a member. Unix answers with kill(2). Windows has no member query and
// reports false, so a caller there sees the leader alone.
//
// Stop is the shared teardown built from those four: wait for the group to
// empty, then Terminate, then Kill. Gone answers its "is it over" question for
// a caller that signals at once.
package procgroup
