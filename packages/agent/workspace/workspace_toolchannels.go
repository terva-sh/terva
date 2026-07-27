package workspace

import (
	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/core"
)

// Everything that lives on a TOOL INSTANCE, and therefore has to be re-bound
// every time the registry is resolved again.
//
// Most session state hangs off a long-lived object and survives a rebuild
// untouched: the confirmer lives on the gate, the extension host-tool
// dispatcher lives on the extension manager. A few channels do not — they are
// fields on the tool itself. `build.Resolve` mints fresh tools, so a rebuild
// hands the agent instances whose channels are nil, and the tool then fails
// for the rest of the session with the front end sitting right there.
//
// This has now been shipped three times, once per channel:
//
//   - the ASKER: ask_user_question answered "no interactive channel" after any
//     rebuild (fixed with SetAsker in rebuildTools)
//   - the ESCALATOR: same shape, same fix
//   - code_execution's HOST CALL: "code_execution is not wired to the approval
//     gate in this session", because WireHostToolDispatcher ran only at session
//     build. Reported from the TUI, but it was never surface-specific — every
//     front end lost it at the first rebuild, and a rebuild fires shortly after
//     almost every session starts, since extensions load in the background.
//
// Each was fixed on its own, next to the last one, without the pairing being
// made explicit anywhere. So they are collected here, and
// TestARebuildKeepsEveryToolChannel enforces the rule generically rather than
// by naming these three — a fourth channel added later fails that test without
// anyone having to remember this comment exists.
//
// The split is by WHEN, not by what, and it is forced: the first half binds on
// the Resolved before its registry is installed, and the second needs the LIVE
// agent, because the gated dispatcher it installs resolves each call's target
// through the agent's current registry at call time rather than the one being
// built. Both halves must run on both paths — session build and every rebuild.

// bindResolvedChannels re-points the channels that bind through the Resolved,
// before its registry is installed on the agent.
func (s *wsSession) bindResolvedChannels(r *build.Resolved) {
	r.SetAsker(&webAsker{s: s})
	r.SetEscalator(&sessionEscalator{s: s})
}

// bindAgentChannels re-points the channels that need the live agent, after its
// registry has been installed.
//
// gate is passed rather than read from s.gate because the session build binds
// this before it has finished assembling itself; a rebuild passes s.gate, which
// is the same gate — it outlives every rebuild by design.
func (s *wsSession) bindAgentChannels(ag *core.Agent, gate *core.ConfirmGate) {
	build.WireHostToolDispatcher(ag, s.extMgr, gate)
}
