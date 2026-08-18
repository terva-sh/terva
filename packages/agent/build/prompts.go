package build

import "terva.sh/terva/packages/i18n"

// System-prompt blocks assembled into an agent at construction time.

// AutoSwarmSystemAddendum is the proactive-delegation NUDGE (Toggle 2): the one
// disposition that isn't self-evident from the swarm_spawn tool description. The
// tool's mechanics (self-contained tasks, no inherited context, don't block,
// when-NOT-to-use, the [auto-swarm update] recap) live in the tool description +
// the recap message; valid Persona names live in the tool's schema enum. So this
// stays short — just the "reach for it proactively" push a bare tool wouldn't
// give. See docs/proposals/web-i18n-authoring.md (sibling auto-swarm discussion).
const AutoSwarmSystemAddendum = `When a request naturally splits into independent sub-tasks that can run concurrently, reach for swarm_spawn proactively rather than doing everything sequentially yourself — spawn one sub-agent per independent task and keep the coordinating work moving in parallel. Sub-agents are also your context shield: when a step would pull large content into your own context (reading a long file, sweeping many files, digesting a big log), delegate it to a sub-agent whose task says what to extract, and work from the concise summary it reports back.`

// SwarmWorktreeSystemAddendum is the environment fact a coordinator cannot see
// and cannot guess: under --swarm-worktrees (config swarm_worktrees) every
// sub-agent boots with --cwd set to its OWN leased git worktree, and the lease
// is RELEASED rather than removed when the child finishes
// (carrier_swarm_worktree.go), so the child's files outlive it in a checkout
// the coordinator never looks at.
//
// Without this the failure is silent and confident. A child reports "I wrote
// packages/foo/bar.go"; the coordinator reads packages/foo/bar.go in its own
// tree, finds its own untouched copy, and concludes the sub-agent did nothing —
// or worse, redoes the work. `git status` agrees with the wrong answer, because
// the changes are on another worktree's branch.
//
// The recap carries each child's worktree path (flushSwarmSummary), so this
// block only has to say what that path MEANS and what may be done with what is
// left in it. Injected only when isolation is actually on: said to a
// shared-tree swarm it would be false, and would send a coordinator hunting for
// a directory that does not exist.
const SwarmWorktreeSystemAddendum = `Each sub-agent works in its own git worktree. A worktree is a separate checkout of this repository on its own branch. The recap gives the worktree path of each sub-agent that finishes.

A file that a sub-agent reports is in that worktree. It is not in your working directory. The same relative path in your tree is a different file. Your own git status does not show the change. You can find your own copy unchanged. That is not evidence that the sub-agent did no work.

Read the file at the worktree path of the sub-agent. You can also compare that worktree with your tree. Do this before you decide that the work is missing. Do this before you do the work a second time.

terva keeps the worktree of a finished sub-agent. You can review it and take what you need. The files that remain there are not a deliverable.

First apply the content to your own tree. You can also confirm that your tree already has the same content. You may then delete those files and remove the worktree.

Do not keep a worktree only because it holds uncommitted changes. Check first whether your tree already has those changes. Tell the user plainly if your tree does not have them.`

// SwarmWorktreeAddendum renders the worktree-isolation block through the
// model-facing prompt catalog, like its auto-swarm and swarm-child siblings.
func SwarmWorktreeAddendum() string {
	return i18n.P("swarm.worktrees.addendum", SwarmWorktreeSystemAddendum)
}

// SwarmChildSystemAddendum is the sub-agent deliverable contract every swarm
// child carries (--swarm-agent mode): the coordinator's auto-swarm recap
// surfaces ONLY the child's final assistant message, so a child that closes
// with task housekeeping instead of its findings reports nothing. The
// 2026-07-08 self-review lost two specialists' findings exactly that way —
// they answered the open-work wrap-up nudge with "all tasks complete" and
// that became the recap. The review-crew charters state the same contract in
// their own voice; this addendum covers every child, Persona or not.
const SwarmChildSystemAddendum = `You are a sub-agent, dispatched by a coordinator agent. The coordinator receives only your final assistant message as your report. Nothing else you wrote reaches it. Always end your task with your complete findings or answer, never with a status update or task-tracker chores. A follow-up prompt can arrive after you have already reported (open tasks, confirmations, wrap-up nudges). Handle it, and restate your full findings in that same reply.`

// SwarmChildAddendum renders the contract through the model-facing prompt
// catalog so operator translations cover it.
func SwarmChildAddendum() string { return i18n.P("swarm.child.addendum", SwarmChildSystemAddendum) }

// DeliverResultSystemAddendum is the structured-deliverable overlay on the
// child contract, pinned only when the spawn carried a schema (and the
// deliver_result tool is therefore registered). It rides ON TOP of
// SwarmChildSystemAddendum: the final prose message still matters for
// humans, but the machine-read report is the tool call.
const DeliverResultSystemAddendum = `Your dispatcher requires a structured deliverable. Before you end your task, call the deliver_result tool exactly once, with your complete findings as its arguments. The schema of the tool is the required shape. If the call reports a validation error, fix the arguments and call the tool again until it succeeds. Your final text message stays a short summary for humans, and the deliver_result call is the machine-read report. terva records a task that ends without a successful deliver_result call as a failed contract.`

// DeliverResultAddendum renders the structured-deliverable overlay through
// the model-facing prompt catalog (same treatment as SwarmChildAddendum).
func DeliverResultAddendum() string {
	return i18n.P("swarm.child.deliver_result", DeliverResultSystemAddendum)
}
