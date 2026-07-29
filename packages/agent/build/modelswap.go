package build

import (
	"terva.sh/terva/packages/agent/tools"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
)

// ModelSwap is the model-swap event: everything that has to move when a live
// session changes model, and — in ApplyModelSwap — the order it moves in.
//
// The fields ARE the list, for the same reason TrustSurfaces' are. Four hosts
// performed this sequence, each spelling it out, and two of them carried four
// separate comments naming the copy they were made from ("mirroring
// Workspace.switchModel", "Mirrors Workspace.switchModel in the web/ctrlproto
// path", "Mirrors Workspace.switchModel (web) and the ACP path"). A comment
// pointing at another file is what a codebase writes when it has no way to say
// "these are the same event".
//
// They had already drifted. The daemon re-pointed every host-routed dispatch
// tool after a swap so a sub-agent spawned next inherits the CURRENT model; the
// other three did not, and the ACP same-endpoint id swap skipped it as well.
// That was harmless only by accident of registration — swarm_spawn, actor_spawn
// and raati_convene are built in workspace_session.go and exist nowhere else —
// and nothing anywhere recorded that as the reason. tools/host_routed.go already
// pins the TOOL side of this contract with compile-time assertions; this is the
// host side of the same contract.
type ModelSwap struct {
	// Agent is the agent being moved. Its usage snapshot is read from here and
	// its tool registry re-pointed through it, even when Swap does the actual
	// client assignment.
	Agent *core.Agent

	// Client is the new provider client, or nil for a SAME-ENDPOINT ID SWAP:
	// the current client keeps serving and only the model id moves. That
	// distinction decides two of the steps below, which is why it is expressed
	// as "is there a new client" rather than a bool a caller can forget to set.
	Client provider.Client

	// Provider, Model, AuthMethod, BaseURL are the target's identity.
	// AuthMethod and BaseURL are read only on the new-client path — an id swap
	// performs no Resolve, so a caller has nothing truthful to put in them, and
	// writing the zero value into terva_status would erase an identity that is
	// still correct.
	Provider, Model, AuthMethod, BaseURL string

	// Swap overrides how the client and model reach the agent. Bot mode is the
	// only caller that needs it: one swap has to fan out across every per-chat
	// agent, which it does through its Loop. Nil means Agent.SetClientAndModel.
	Swap func(client provider.Client, model string)

	// After is this host's own tail — recording the new model on the session,
	// telling clients, whatever only it has.
	After func()
}

// ApplyModelSwap moves one agent onto a different model.
//
// The order carries one hard constraint and one soft one. HARD: the usage
// snapshot is read from the OLD client and seeded into the NEW one before the
// new one is installed, or the status meters blank until the next turn's headers
// arrive. SOFT: everything the model can SEE of its own routing — terva_status,
// the dispatch tools — is re-pointed after the swap itself, so nothing observes
// a half-moved agent.
//
// Every step is nil-safe and no-ops where it does not apply, which is what lets
// the careful version be the only version: a host with no host-routed tools runs
// an empty loop rather than being trusted to remember it has none.
func ApplyModelSwap(s ModelSwap) {
	if s.Agent == nil {
		return
	}

	if s.Client != nil {
		// 1. Carry the passively-observed usage across the rebuild. The seeder
		//    rejects a foreign provider's snapshot itself, so this is safe on a
		//    cross-provider swap and merely does nothing.
		if snap, ok := s.Agent.Usage(); ok {
			provider.SeedClientUsage(s.Client, snap)
		}
		// 2. Install the new client and model.
		if s.Swap != nil {
			s.Swap(s.Client, s.Model)
		} else {
			s.Agent.SetClientAndModel(s.Client, s.Model)
		}
		// 3. A client swap keeps the agent's registry, so terva_status still
		//    reports the PREVIOUS provider identity — and because
		//    FindModel(oldProvider, newModel) then misses, it also loses the
		//    context-window size. Only on this path: see AuthMethod's note.
		if st, ok := s.Agent.LookupTool("terva_status"); ok {
			if stt, ok := st.(*tools.StatusTool); ok {
				stt.SetProvider(s.Provider, s.AuthMethod, s.BaseURL)
			}
		}
	} else {
		// Same endpoint, new id: the client is still correct, and so is
		// everything derived from its identity.
		s.Agent.SetModel(s.Model)
	}

	// 4. Re-point every host-routed dispatch tool, on BOTH paths — an id swap
	//    changes what a sub-agent should inherit just as much as a rebuild
	//    does. Generic over tools.HostRouted so this covers swarm_spawn,
	//    actor_spawn, raati_convene and anything added later without a fifth
	//    host having to learn about it.
	for _, tl := range s.Agent.ToolsSnapshot() {
		if hr, ok := tl.(tools.HostRouted); ok {
			hr.SetHost(s.Provider, s.Model)
		}
	}

	// 5. Whatever only this host has.
	if s.After != nil {
		s.After()
	}
}
