package core

import "terva.sh/terva/packages/provider"

// ResumeState says whether a transcript is waiting for the model rather than for
// the user, and why. A turn that dies on a provider error leaves the session
// stranded: the work is recorded, nothing is running, and no one is going to
// send the next request without being asked.
//
// The distinction it draws is not visible in message roles. A transcript ending
// on an assistant message is the ordinary idle state AND the cut-short state, and
// a transcript ending on RoleUser is a waiting prompt, a compaction summary, or a
// clear divider depending on metadata. Every surface that offers to resume needs
// the same answer, so it is computed once here instead of in each of them.
type ResumeState int

const (
	// ResumeNotStuck is an ordinary idle session. The last thing in it is a
	// finished reply, a compaction summary, or a clear divider, and the next move
	// belongs to the user.
	ResumeNotStuck ResumeState = iota
	// ResumeAfterUser is a prompt that never got an answer. This is the common
	// one: the provider failed before anything streamed, so the transcript ends
	// on the words the user typed.
	ResumeAfterUser
	// ResumeAfterTools is a completed tool round whose follow-up call never
	// landed. The tool results are on the transcript and the model has not read
	// them.
	ResumeAfterTools
	// ResumeAfterCutShort is a reply the provider interrupted partway, marked by
	// [MetaIncomplete]. Resuming it extends that message rather than starting a
	// new one, which needs a provider that continues an assistant prefill.
	ResumeAfterCutShort
)

// Stuck reports whether the session needs a nudge to move.
func (s ResumeState) Stuck() bool { return s != ResumeNotStuck }

// String names the state for logs and test failures.
func (s ResumeState) String() string {
	switch s {
	case ResumeAfterUser:
		return "after-user"
	case ResumeAfterTools:
		return "after-tools"
	case ResumeAfterCutShort:
		return "after-cut-short"
	default:
		return "not-stuck"
	}
}

// ResumeStateOf classifies a transcript by its last message.
//
// The RoleUser arm is the subtle one, because four machine-authored messages
// carry that role and they do not mean the same thing here. A tool-image mirror
// is appended inside a tool round, so a transcript ending on one is stranded
// exactly like a transcript ending on the tool results it mirrors. A compaction
// summary or a clear divider is the opposite: both leave a session that is idle
// and healthy, and offering to resume either would generate a reply to nothing.
// A host-injected nudge was written to drive a turn, so it counts as stuck.
//
// [IsUserTurn] cannot be used for this. It answers "did the user say this", and
// it groups all four together as not-the-user, which is the right answer to its
// own question and the wrong one to this one.
//
// A trailing assistant message holding unanswered tool calls is a fifth shape,
// left deliberately unclassified. It comes from the process dying between the
// reply and the tool run rather than from a provider error, and resuming it
// means presenting an unmatched tool_use to the next request.
func ResumeStateOf(msgs []provider.Message) ResumeState {
	if len(msgs) == 0 {
		return ResumeNotStuck
	}
	last := msgs[len(msgs)-1]
	switch last.Role {
	case provider.RoleTool:
		return ResumeAfterTools
	case provider.RoleAssistant:
		if last.Meta[MetaIncomplete] == "true" {
			return ResumeAfterCutShort
		}
		return ResumeNotStuck
	case provider.RoleUser:
		if IsToolImageMirror(last) {
			return ResumeAfterTools
		}
		if last.Meta[MetaCompaction] == "true" || last.Meta[MetaClear] != "" {
			return ResumeNotStuck
		}
		return ResumeAfterUser
	}
	return ResumeNotStuck
}
