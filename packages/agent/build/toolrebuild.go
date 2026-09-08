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

	// ReadOnly is retained for source compatibility.
	// Deprecated: rebuilds derive classification from the current tools.
	ReadOnly *core.ReadOnlySet
	// Gate is shared across generations; script bindings use it for approval.
	Gate *core.ConfirmGate

	// Tasks is the session's task controller. A fresh resolve carries a fresh
	// one over an unbound store; adopting it would leave the model writing
	// tasks it can no longer see and the durable board frozen.
	Tasks *tasktool.Controller

	// Memory is the session's durable-memory tool, holding the bound stores.
	// Same rule as Tasks: a fresh resolve mints fresh stores, and adopting them
	// would leave the model writing facts that no pane and no injected block
	// reads — memory would appear to work and quietly go nowhere.
	Memory *tools.MemoryTool

	// Files is the session's file-state tracker (what the model has seen of
	// each path). Same rule as Tasks and Memory: a fresh resolve mints an empty
	// one, and the edit tool's staleness note would then claim files were never
	// read that the model read minutes earlier.
	Files *tools.FileState

	// Sandbox is the session's sandbox — the object /jail and /unjail mutate.
	// Rebuilt tools must consult the same one, or those verbs report success
	// and adjust something nothing reads.
	Sandbox *tools.Sandbox

	// TicketCard is the session's per-turn ticket card. Same rule as Tasks,
	// Memory, and Files, with one extra edge: the ephemeral tail is wired once at
	// session build, so a fresh resolve's fresh card would be invalidated by
	// every ticket write and rendered by nobody. The card would then freeze at
	// whatever the store held when the session started, which is worse than no
	// card because it is confidently out of date. nil for a host with no store,
	// and UseTicketCard tolerates that.
	TicketCard *tools.TicketCard

	// Ext and MCP are the two tool sources a fresh Resolve knows nothing about.
	// Order is load-bearing and matches the build order: MergeExtensionTools is
	// first-write-wins, so merging extensions first keeps an extension tool
	// winning a name collision against an MCP server's.
	Ext *extensions.Manager
	MCP *MCPToolAdapter

	// Asker is the front-end question channel to carry onto a fresh registry.
	// Hosts that negotiate a channel after a rebuild keep it here rather than
	// reading the agent concurrently while the registry is being published.
	Asker core.Asker
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
	r.UseTasks(s.Tasks)
	r.UseMemory(s.Memory)
	r.UseFiles(s.Files)
	r.UseSandbox(s.Sandbox)
	r.UseTicketCard(s.TicketCard)
	if s.Ext != nil {
		r.MergeExtensionTools(&ExtToolAdapter{Mgr: s.Ext})
	}
	if s.MCP != nil {
		r.MergeExtensionTools(s.MCP)
	}
	// A front-end channel belongs to the live session, not to a resolved tool
	// registry. Carry the explicit survivor onto every fresh registry so
	// extension reloads cannot turn a negotiated RPC question channel into
	// no_channel or race a concurrent channel bind through ag.Asker.
	r.SetAsker(s.Asker)
	return r.PublishTools(ag, s.Gate)
}
