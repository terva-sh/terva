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
