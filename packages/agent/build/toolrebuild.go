package build

import (
	"terva.sh/terva/packages/agent/extensions"
	"terva.sh/terva/packages/agent/tools"
	"terva.sh/terva/packages/agent/tools/tasks/tasktool"
	"terva.sh/terva/packages/core"
)

// LiveToolSet is the rebuild-survivor rule in one place.
//
// Re-resolving is how a host refreshes the model's tool set after something
// changed underneath it — an extension reload, a Workspace Trust flip. But a
// fresh Resolve is a fresh EVERYTHING: a registry holding only the built-ins, a
// new sandbox, a new read-only set, a new task controller. Each long-lived
// object the session assembled by hand has to be put BACK onto it, or the
// rebuild quietly takes that thing away from the model — and the failure is
// invisible, because the tool set it produces is perfectly valid, just smaller
// and pointed at objects nothing else holds.
//
// The rule used to be spelled out once per host. Its history is a series of
// shipped bugs, each one a survivor added to a single site: the editor's MCP
// tools disappeared from an rpc worker the moment its extensions finished
// loading, because a fresh Resolve carries no MCP and rpc's closure never
// re-merged the adapter. That one slipped past the source-reading guard built to
// catch exactly this class, since the guard recognised a survivor by an
// Adopt*/Use* naming convention and re-merging MCP is spelled
// MergeExtensionTools.
//
// So it lives here, and the fixed-shape hosts (rpc, acp) call it rather than
// each keeping a copy. The workspace daemon still has its own — it re-binds
// front-end channels and emits a prompt-rebuilt notice, neither of which exists
// off the daemon — and rpc_reload_twin_test.go holds the two in step.
//
// Every field is optional; a nil one is simply nothing to carry.
type LiveToolSet struct {
	// Args is re-resolved to produce the fresh base registry. It carries the
	// approval mode and, through the trust store or a pin, the Workspace Trust
	// verdict the new tool set is gated on.
	//
	// It is read when Rebuild RUNS, not when the session is built, and that
	// distinction is load-bearing: a fresh Resolve re-mints every tool that
	// carries the session's identity — terva_status's provider/auth/base URL,
	// and read's SupportsVision, which is the resolved model's image
	// capability. A host whose model can move mid-session must therefore hand
	// this struct the CURRENT pair, not the one it launched with, or the
	// rebuild reverts the swap. Freezing the launch pair into a long-lived
	// LiveToolSet is how both fixed-shape hosts came to do exactly that; both
	// now assemble the struct inside their rebuild closure.
	Args Args

	// ReadOnly is the LIVE gate's read-only set, not the throwaway one a fresh
	// Resolve mints. MergeToolsForMode records each read_only tool's name in
	// whichever set the Resolved carries, and the confirm gate reads the live
	// one — so without this an extension's read-only tools start prompting
	// after any rebuild.
	ReadOnly *core.ReadOnlySet

	// Tasks is the session's task controller. A fresh resolve carries a fresh
	// one over an unbound store; adopting it would leave the model writing
	// tasks it can no longer see and the durable board frozen.
	Tasks *tasktool.Controller

	// Sandbox is the session's sandbox — the object /jail and /unjail mutate.
	// Rebuilt tools must consult the same one, or those verbs report success
	// and adjust something nothing reads.
	Sandbox *tools.Sandbox

	// Ext and MCP are the two tool sources a fresh Resolve knows nothing about.
	// Order is load-bearing and matches the build order: MergeExtensionTools is
	// first-write-wins, so merging extensions first keeps an extension tool
	// winning a name collision against an MCP server's.
	Ext *extensions.Manager
	MCP *MCPToolAdapter
}

// Rebuild re-resolves and swaps the result onto ag, reporting whether the
// model-facing surface actually changed (ag.SetTools' answer). A resolve that
// fails leaves the current tool set standing and reports false: a session that
// keeps the tools it has is strictly better than one that loses them to a
// transient config error.
func (s LiveToolSet) Rebuild(ag *core.Agent) bool {
	if ag == nil {
		return false
	}
	r, err := Resolve(s.Args, true)
	if err != nil {
		return false
	}
	r.AdoptReadOnlySet(s.ReadOnly)
	r.UseTasks(s.Tasks)
	r.UseSandbox(s.Sandbox)
	if s.Ext != nil {
		r.MergeExtensionTools(&ExtToolAdapter{Mgr: s.Ext})
	}
	if s.MCP != nil {
		r.MergeExtensionTools(s.MCP)
	}
	return ag.SetTools(r.ToolRegistry)
}
