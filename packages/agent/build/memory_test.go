package build

import (
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/permissions"
	"terva.sh/terva/packages/agent/tools"
	"terva.sh/terva/packages/agent/tools/memory"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/testsupport"
)

// memResolved builds a Resolved carrying a memory tool over in-memory stores and
// archives — enough to exercise the block-rendering, refresh and recall seams
// without a home. Unbound on both tiers, so nothing touches disk.
func memResolved(t *testing.T) (*Resolved, *tools.MemoryTool) {
	t.Helper()
	mt := &tools.MemoryTool{
		Project: memory.NewStore(), User: memory.NewUserStore(),
		ProjectArchive: memory.NewArchive(memory.ScopeProject, memory.LabelProject),
		UserArchive:    memory.NewArchive(memory.ScopeUser, memory.LabelUser),
	}
	r := &Resolved{ToolRegistry: core.Registry{"memory": mt}}
	return r, mt
}

// An empty memory renders nothing: a policy telling the model to curate a
// memory that has nothing in it costs cached-prefix bytes on every request and
// says nothing the tool description does not.
func TestMemoryBlockEmptyWhenNothingStored(t *testing.T) {
	r, _ := memResolved(t)
	if got := MemoryBlock(r); got != "" {
		t.Fatalf("MemoryBlock with no entries = %q, want empty", got)
	}
}

func TestMemoryBlockCarriesBothScopes(t *testing.T) {
	r, mt := memResolved(t)
	if err := mt.Project.Add("uses pnpm, not npm"); err != nil {
		t.Fatal(err)
	}
	if err := mt.User.Add("prefers worktrees over branch switching"); err != nil {
		t.Fatal(err)
	}
	got := MemoryBlock(r)
	for _, want := range []string{"uses pnpm, not npm", "prefers worktrees over branch switching"} {
		if !strings.Contains(got, want) {
			t.Errorf("block missing %q:\n%s", want, got)
		}
	}
	// User first: it is short and stable, so a clamp trims the project tail
	// rather than the user's own facts.
	if strings.Index(got, "prefers worktrees") > strings.Index(got, "uses pnpm") {
		t.Errorf("project section precedes user section:\n%s", got)
	}
}

// --no-memory means OFF: no tool in the registry at all, so nothing can be
// written and no block can be rendered. A tool still recording facts the user
// opted out of would not be "no memory functionality available".
func TestNoMemoryRemovesTheTool(t *testing.T) {
	cwd := testsupport.TempDir(t)
	sandbox := tools.NewSandbox(cwd)

	on := BuildToolRegistry(Args{}, core.ApprovalAsk, cwd, sandbox, "anthropic", "key", false, nil)
	if _, ok := on["memory"]; !ok {
		t.Fatal("memory tool absent by default; it should be on")
	}
	off := BuildToolRegistry(Args{NoMemory: true}, core.ApprovalAsk, cwd, sandbox, "anthropic", "key", false, nil)
	if _, ok := off["memory"]; ok {
		t.Error("--no-memory left the memory tool registered")
	}
	// And with no tool, the block is empty by construction.
	if got := MemoryBlock(&Resolved{ToolRegistry: off}); got != "" {
		t.Errorf("--no-memory still rendered a block: %q", got)
	}
}

// memory writes only terva's own state under $TERVA_HOME, never the workspace,
// so it classifies like the task tools: auto-admitting and available in plan
// mode. A model that cannot record what it just learned, in the mode meant for
// investigation, loses exactly the findings that mode exists to produce.
func TestMemorySurvivesPlanMode(t *testing.T) {
	if !permissions.IsReadOnly("memory") {
		t.Fatal("memory is not classified read-only; plan mode will prune it")
	}
	if !permissions.IsBuiltin("memory") {
		t.Error("memory is not classified builtin; workspace mode will prompt for it")
	}
	cwd := testsupport.TempDir(t)
	reg := BuildToolRegistry(Args{}, core.ApprovalPlan, cwd, tools.NewSandbox(cwd), "anthropic", "key", false, nil)
	if _, ok := reg["memory"]; !ok {
		t.Error("plan mode pruned the memory tool")
	}
}

// The block and the tool must read one store per session. If MemoryBlock built
// its own, a mid-session write would be invisible to the next refresh.
func TestMemoryBlockReadsTheRegistrysStore(t *testing.T) {
	r, mt := memResolved(t)
	if err := mt.Project.Add("first fact"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(MemoryBlock(r), "first fact") {
		t.Fatal("block does not reflect the tool's store")
	}
	if err := mt.Project.Add("second fact"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(MemoryBlock(r), "second fact") {
		t.Error("block went stale against the tool's store")
	}
}

// RefreshMemory must tolerate every shape a caller can hand it: memory off, no
// resolved build, a registry holding something else under the name.
func TestRefreshMemoryIsSafeWhenMemoryIsAbsent(t *testing.T) {
	RefreshMemory(nil)
	RefreshMemory(&Resolved{ToolRegistry: core.Registry{}})
	RefreshMemory(&Resolved{ToolRegistry: core.Registry{"memory": &tools.AskUserTool{}}})
	if got := MemoryBlock(&Resolved{ToolRegistry: core.Registry{"memory": &tools.AskUserTool{}}}); got != "" {
		t.Errorf("a non-memory tool under the name rendered a block: %q", got)
	}
}

// fakeExtSource is an ExtensionToolSource advertising a fixed tool list.
// readOnly defaults false in a zero value, so the constructor-free literals
// used by most tests set it explicitly via the helper below.
type fakeExtSource struct {
	names    []string
	readOnly bool
}

func (f fakeExtSource) Tools() []ExtensionToolInfo {
	out := make([]ExtensionToolInfo, 0, len(f.names))
	for _, n := range f.names {
		out = append(out, ExtensionToolInfo{Extension: "ext", Name: n, ReadOnly: f.readOnly})
	}
	return out
}
func (f fakeExtSource) NewExtensionTool(ExtensionToolInfo) core.Tool { return &tools.AskUserTool{} }

// Both installed means two blocks injected and two write paths to the same
// files. Core defers rather than winning, so upgrading terva never breaks a user
// who has pinned the old extension — and the tool-name collision resolves
// core-first, which would otherwise leave the extension's tool unregistered
// while its injected block kept arriving.
func TestCoreMemoryStandsDownForTheExtension(t *testing.T) {
	r, mt := memResolved(t)
	if err := mt.Project.Add("a fact"); err != nil {
		t.Fatal(err)
	}
	if MemoryBlock(r) == "" {
		t.Fatal("precondition: core memory should be live")
	}

	r.standDownForExtensionMemory(fakeExtSource{names: []string{"memory", "other"}})
	if _, ok := r.ToolRegistry["memory"]; ok {
		t.Error("core memory tool survived an extension-provided memory")
	}
	if got := MemoryBlock(r); got != "" {
		t.Errorf("core still injected a block after standing down: %q", got)
	}
}

// The stand-down exists so the EXTENSION's memory tool takes the slot — not so
// the session ends up with no memory tool at all.
//
// Every other test here calls standDownForExtensionMemory directly, which is
// why this went unnoticed: the only production caller is MergeExtensionTools,
// and it merges FIRST. The merge skips the extension's `memory` because core's
// already occupies the name (built-ins win on conflict), and the stand-down
// then deletes core's. Both halves are individually correct; composed in that
// order they leave the name empty.
func TestAMemoryExtensionTakesTheSlotRatherThanEmptyingIt(t *testing.T) {
	r, _ := memResolved(t)
	r.MergeExtensionTools(fakeExtSource{names: []string{"memory", "other"}})

	tool, ok := r.ToolRegistry["memory"]
	if !ok {
		t.Fatal("no memory tool at all: core stood down and the extension's never registered")
	}
	if _, isCore := tool.(*tools.MemoryTool); isCore {
		t.Error("core's memory survived; the extension's should hold the slot")
	}
	if got := MemoryBlock(r); got != "" {
		t.Errorf("core still injected a block after standing down: %q", got)
	}
}

// Plan mode is the case the reorder must not break. Core's memory is
// deliberately alive in plan mode (it writes only $TERVA_HOME), but a
// non-read-only extension tool is skipped by the merge — so standing down for
// one that will never register would remove memory from exactly the mode that
// most needs to record what it learned.
func TestCoreMemorySurvivesAnExtensionToolPlanModeWouldSkip(t *testing.T) {
	r, _ := memResolved(t)
	r.ApprovalMode = core.ApprovalPlan
	r.MergeExtensionTools(fakeExtSource{names: []string{"memory"}, readOnly: false})

	if _, ok := r.ToolRegistry["memory"]; !ok {
		t.Fatal("plan mode lost memory: core stood down for a tool the merge skips")
	}
}

// An extension set without memory must leave core's alone — the stand-down is
// for one specific collision, not a general deference to extensions.
func TestCoreMemorySurvivesUnrelatedExtensions(t *testing.T) {
	r, mt := memResolved(t)
	if err := mt.Project.Add("a fact"); err != nil {
		t.Fatal(err)
	}
	r.standDownForExtensionMemory(fakeExtSource{names: []string{"mail_send", "gh_pr"}})
	if _, ok := r.ToolRegistry["memory"]; !ok {
		t.Fatal("core memory removed by an unrelated extension set")
	}
	if MemoryBlock(r) == "" {
		t.Error("core stopped injecting after an unrelated merge")
	}
}

// Nil-tolerant on every axis a caller can hand it, including a registry whose
// "memory" is somebody else's tool — which must not be deleted on our behalf.
func TestStandDownIsSafeOnOddInputs(t *testing.T) {
	var nilR *Resolved
	nilR.standDownForExtensionMemory(fakeExtSource{names: []string{"memory"}})

	r, _ := memResolved(t)
	r.standDownForExtensionMemory(nil)
	if _, ok := r.ToolRegistry["memory"]; !ok {
		t.Error("a nil source removed core memory")
	}

	foreign := &Resolved{ToolRegistry: core.Registry{"memory": &tools.AskUserTool{}}}
	foreign.standDownForExtensionMemory(fakeExtSource{names: []string{"memory"}})
	if _, ok := foreign.ToolRegistry["memory"]; !ok {
		t.Error("stand-down deleted a memory tool that was not core's to remove")
	}
}
