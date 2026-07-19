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

// SwarmChildSystemAddendum is the sub-agent deliverable contract every swarm
// child carries (--swarm-agent mode): the coordinator's auto-swarm recap
// surfaces ONLY the child's final assistant message, so a child that closes
// with task housekeeping instead of its findings reports nothing. The
// 2026-07-08 self-review lost two specialists' findings exactly that way —
// they answered the open-work wrap-up nudge with "all tasks complete" and
// that became the recap. The review-crew charters state the same contract in
// their own voice; this addendum covers every child, Persona or not.
const SwarmChildSystemAddendum = `You are a sub-agent dispatched by a coordinating agent. The coordinator receives ONLY your final assistant message as your report — nothing else you wrote reaches it. Always end your task with your complete findings or answer; never end on a status update or task-tracker housekeeping. If a follow-up prompt arrives after you have already reported (open tasks, confirmations, wrap-up nudges), handle it and restate your full findings in that same reply.`

// SwarmChildAddendum renders the contract through the model-facing prompt
// catalog so operator translations cover it.
func SwarmChildAddendum() string { return i18n.P("swarm.child.addendum", SwarmChildSystemAddendum) }

// DeliverResultSystemAddendum is the structured-deliverable overlay on the
// child contract, pinned only when the spawn carried a schema (and the
// deliver_result tool is therefore registered). It rides ON TOP of
// SwarmChildSystemAddendum: the final prose message still matters for
// humans, but the machine-read report is the tool call.
const DeliverResultSystemAddendum = `Your dispatcher requires a STRUCTURED deliverable. Before ending your task, call the deliver_result tool exactly once with your complete findings as its arguments — the tool's schema is the required shape. If the call reports a validation error, fix the arguments and call it again until it succeeds. Your final text message remains a short prose summary for humans; the deliver_result call is the report the dispatcher machine-reads. A task that ends without a successful deliver_result call is recorded as contract-NOT-met.`

// DeliverResultAddendum renders the structured-deliverable overlay through
// the model-facing prompt catalog (same treatment as SwarmChildAddendum).
func DeliverResultAddendum() string {
	return i18n.P("swarm.child.deliver_result", DeliverResultSystemAddendum)
}
