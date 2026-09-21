// Package sessionlock keeps two terva processes from writing one session
// transcript at the same time, and says out loud why a session is held.
//
// # What was wrong
//
// core.openSession took a plain O_APPEND|O_WRONLY handle on a transcript with
// nothing to exclude a second process. Byte integrity survived — O_APPEND plus
// a flush per row makes each row one small write at the end of the file — but
// the transcript's meaning did not. Two processes each replay the file into
// their own memory, so they write amend rows whose indices are counted against
// different transcripts, and meta rows where the last writer wins a race
// neither knew it was in.
//
// # Two files, and why they cannot be one
//
// A locked transcript grows two neighbours that share its stem:
//
//	<stem>.jsonl        the transcript. Never locked, never chmod'd.
//	<stem>.lock         a zero-byte flock guard.
//	<stem>.lock.json    the claim record.
//
// The record must be written atomically, because a second process reads it
// WITHOUT holding anything in order to print a refusal, and a torn read there
// would report nonsense about who holds the session. Atomic means write a temp
// file and rename it over the target, and a rename replaces the inode. An flock
// lives on an inode. So one file cannot be both: the rename would move the lock
// out from under whoever holds it, and two processes would each believe they
// had it. packages/agent/worktree splits registry.lock from registry.json for
// this reason, and packages/agent/config does the same for config.json.
//
// # Which one is authoritative
//
// The flock is. It answers "is a process writing this right now", the kernel
// drops it the instant that process dies, and no timeout of ours is involved.
// That is the property packages/filelock was written to defend and this package
// does not trade away.
//
// The record is advisory, and it carries the three things an flock has no room
// for: a reason a person can read, the name of whoever holds it, and a claim
// somebody made deliberately that is meant to outlive the process that made it.
// An expiry exists for that last case, because a claim with no process behind
// it has no other way to end.
//
// # Recovering from a crash, strongest witness first
//
//  1. The holder died. The kernel dropped the flock. Nothing to wait for.
//  2. The pid is gone AND the heartbeat is stale. Consulted only where flock
//     does not work (NFS without lockd, some FUSE mounts), and only together:
//     a pid outlives the process that owned it, so on its own it would call a
//     live holder dead the moment the number was reused.
//  3. The claim expired. The only recovery available for an explicit claim.
//
// # What this does not do
//
// It does not stop a terva old enough to predate it. That binary does not know
// the files exist and will open the transcript and write. The guarantee holds
// between two binaries that both carry this package. Making the transcript
// read-only while held would bind an old binary, and would also break
// read-only replay, break tail -f, and leave an unwritable session behind after
// a crash. The cure is worse than the disease.
//
// It also does not reduce the number of clients one daemon serves. A second
// browser tab and an attached TUI share a session through one process and one
// handle, which is the documented design; this package is about the second
// PROCESS.
package sessionlock
