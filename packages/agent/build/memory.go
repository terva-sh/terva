package build

import (
	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/agent/tools"
	"terva.sh/terva/packages/agent/tools/memory"
	"terva.sh/terva/packages/core"
)

// newMemoryTool binds the two memory stores for this session's project and
// returns the tool over them, or nil when there is no terva home to store
// anything in.
//
// Adoption runs first and once per session: a user who has been running the
// memory EXTENSION keeps their facts when it is retired. It copies rather than
// moves, so a rollback finds the extension's data intact — see memory.Adopt.
// A failure there is deliberately non-fatal; starting with an empty memory beats
// refusing to start.
//
// Binding failures are non-fatal for the same reason: an unbindable store stays
// in-memory, so the tool still answers and the session still runs. The facts
// just don't outlive it.
func newMemoryTool(cwd string) *tools.MemoryTool {
	home := config.TervaHome()
	if home == "" {
		return nil
	}
	_ = memory.Adopt(home, cwd)

	userDir := memory.UserDir(home)
	user := memory.NewUserStore()
	_ = user.Rebind(userDir)

	// An empty dir (no resolvable cwd) leaves the project store unbound, which
	// Rebind reads as "no target". That is deliberate: project facts written
	// into a shared bucket would surface in an unrelated repo.
	projectDir := memory.ProjectDir(home, cwd)
	project := memory.NewStore()
	_ = project.Rebind(projectDir)

	// The archives bind off the SAME two directories, so an unbound scope has an
	// unbound archive and the two tiers of one scope can never end up pointing at
	// different projects. ArchiveDir("") is "", which Rebind reads as in-memory.
	userArchive := memory.NewArchive(memory.ScopeUser, memory.LabelUser)
	_ = userArchive.Rebind(memory.ArchiveDir(userDir))
	projectArchive := memory.NewArchive(memory.ScopeProject, memory.LabelProject)
	_ = projectArchive.Rebind(memory.ArchiveDir(projectDir))

	return &tools.MemoryTool{
		Project: project, User: user,
		ProjectArchive: projectArchive, UserArchive: userArchive,
	}
}

// memoryTool is the session's memory tool, or nil when memory is off, absent, or
// an extension is providing it instead.
//
// Every read of the registry goes through here for the reason MemoryBlock gives:
// the block, the refresh and the recall must not disagree about what is stored,
// and the registry entry is the one binding a session has.
func (r *Resolved) memoryTool() *tools.MemoryTool {
	if r == nil || r.ToolRegistry == nil {
		return nil
	}
	mt, _ := r.ToolRegistry["memory"].(*tools.MemoryTool)
	return mt
}

// MemoryRecall returns the per-turn provider for the ARCHIVED tier: the entries
// whose keys match what has just been said, rendered for the uncached tail.
//
// This is the other half of the cache split. MemoryBlock is the constant half —
// it rides the cached system prefix and is unchanged by any of this. Archived
// entries are absent from that prefix entirely and cost nothing until a turn's
// own words reach them, which is what lets the archive be orders of magnitude
// larger than the block.
//
// Returns nil only when there is no memory tool at all (memory off, or an
// extension providing it). Deliberately NOT gated on the archives being empty at
// build time: the closure reads them live, so an entry archived mid-session
// starts firing on the very next turn. Gating would have frozen the tier for the
// rest of any session that began with an empty archive — which is every session
// in which the feature gets used for the first time.
//
// The cost of that choice is one closure per session returning "" until
// something is archived, and composeTail drops an empty host block, so it adds
// nothing to the wire.
//
// record=false is the sizing twin behind /context: it renders the identical
// block but does not overwrite the activation trace, so opening a size view
// cannot replace what the last real turn recalled with a result computed against
// a transcript nobody sent.
func (r *Resolved) MemoryRecall(ag *core.Agent, record bool) func() string {
	mt := r.memoryTool()
	if mt == nil {
		return nil
	}
	project, user := mt.ProjectArchive, mt.UserArchive
	if project == nil && user == nil {
		return nil
	}
	return func() string {
		block, fired := memory.Recall(recentLoreScan(ag.Messages()), project, user)
		if record {
			mt.SetLastRecall(fired)
		}
		return block
	}
}

// MemoryBlock renders the memory a session should be shown, for the cached
// system-prompt segment. Empty when memory is off, unavailable, or both scopes
// are empty — an empty segment is dropped rather than paying for a policy about
// a memory that has nothing in it.
//
// Read from the registry's own tool so the block and the tool cannot disagree
// about what is stored: one bind per session, one source of truth.
func MemoryBlock(r *Resolved) string {
	mt := r.memoryTool()
	if mt == nil {
		return ""
	}
	var user, project []string
	if mt.User != nil {
		user = mt.User.List()
	}
	if mt.Project != nil {
		project = mt.Project.List()
	}
	return memory.RenderBlock(user, project)
}

// RefreshMemory re-reads both scopes from disk and re-renders the system prompt.
// Called at the two moments the frozen block goes stale: a session boundary, and
// a compaction (which summarizes away the tool results that showed this
// session's writes, leaving the model unable to see what it saved).
//
// Deliberately NOT called per write. Memory rides the cached system prefix, so
// re-injecting on every mutation would re-cache the prompt each time a fact is
// saved; the model sees its own change through the tool's return value instead.
// The inactive-tool-groups note is the cautionary twin here — it went the other
// way, refreshed every turn, and trained the model to narrate it in half of all
// messages for a whole session.
// The archives are reloaded here too, even though nothing about them is frozen
// — they are read live every turn. It costs one directory walk at a session
// boundary and means a hand-edit to an entry file, or another instance's
// archive, is picked up at the same moment as everything else rather than at
// whatever later point something happened to re-read.
func RefreshMemory(r *Resolved) {
	mt := r.memoryTool()
	if mt == nil {
		return
	}
	if mt.User != nil {
		_ = mt.User.Reload()
	}
	if mt.Project != nil {
		_ = mt.Project.Reload()
	}
	_ = mt.UserArchive.Reload()
	_ = mt.ProjectArchive.Reload()
	r.rebuildSystemPrompt()
}

// standDownForExtensionMemory removes core's memory tool when an extension is
// still providing one, so exactly one memory is live at a time.
//
// The signal is a registered tool named "memory" from any extension. That is the
// same name the retired extension used, and the same name core registers, so a
// third-party extension choosing it would also win — which is the right outcome:
// two things called memory in one session is the failure being prevented, and
// the one the user installed deliberately should be it.
//
// Deleting from the registry is enough to disable everything: MemoryBlock reads
// the registry's tool, so the block goes with it.
func (r *Resolved) standDownForExtensionMemory(mgr ExtensionToolSource) {
	if r == nil || mgr == nil || r.ToolRegistry == nil {
		return
	}
	if _, ours := r.ToolRegistry["memory"].(*tools.MemoryTool); !ours {
		return // not core's to stand down
	}
	for _, info := range mgr.Tools() {
		// ExtToolRegisters, not just a name match: standing down for a tool
		// the merge will skip is how memory disappears entirely. Plan mode is
		// the live case — it admits only read-only extension tools, while
		// core's memory is deliberately alive there because it writes nothing
		// but $TERVA_HOME. Deferring to a tool that never arrives would strip
		// memory from exactly the mode whose findings most need recording.
		if info.Name == "memory" && ExtToolRegisters(info, r.ApprovalMode) {
			delete(r.ToolRegistry, "memory")
			return
		}
	}
}
