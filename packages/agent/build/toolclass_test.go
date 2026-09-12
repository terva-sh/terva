package build

import (
	"testing"

	ticket "github.com/terva-sh/git-ticket/ticket"

	"terva.sh/terva/packages/agent/permissions"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/testsupport"
)

// The tool-classification maps (builtin, readOnly, editTools,
// interactive) are what the permission ladder trusts, and nothing tied
// them to the registry they describe — 10-permissions.md flagged the
// duplication, and grounding it found live drift: several first-party tools
// are registered but absent from the trusted-origin set, with nothing
// recording whether that was chosen. These tests pin the relations and turn
// each absence into a named, reasoned entry — changing one becomes a
// decision made in a diff, not an accident of the maps.

// registeredElsewhere names every first-party tool that does NOT come out of
// a plain BuildToolRegistry call, and where it registers instead. The
// universe below is the registry's keys plus these.
var registeredElsewhere = map[string]string{
	"skill":          "Resolve, gated on !NoSkill",
	"task_list":      "Resolve's task-tool block",
	"task_create":    "Resolve's task-tool block",
	"task_update":    "Resolve's task-tool block",
	"task_archive":   "Resolve's task-tool block",
	"activate_tools": "Resolve, iff LazyTools",
	"deliver_result": "Resolve, swarm children carrying a deliverable schema",
	"generate_image": "BuildToolRegistry, gated on a configured imagegen backend",
	// The workspace host's injectExtraTools set (workspace_session.go) — the
	// only registrant of all of these.
	"swarm_spawn":       "workspace injectExtraTools, iff auto-swarm enabled",
	"actor_spawn":       "workspace injectExtraTools (play sessions)",
	"raati_convene":     "workspace injectExtraTools",
	"world_note":        "workspace injectExtraTools (--play only)",
	"world_reveal":      "workspace injectExtraTools (--play only)",
	"terva_restart":     "workspace injectExtraTools, iff relaunch enabled",
	"terva_arm_restart": "workspace injectExtraTools, iff relaunch enabled",
	"chat_send_image":   "workspace injectExtraTools, iff a chat bridge is bound",
	"chat_send_file":    "workspace injectExtraTools, iff a chat bridge is bound",
	"share_file":        "workspace injectExtraTools — every session the workspace host serves",
}

// outsideTrustedOrigin is the first-party set deliberately absent from
// builtin: in workspace approval mode these prompt as FOREIGN tools, by
// decision (maintainer call, 2026-07-27 — the same audit moved the four
// play/deliberation tools INTO the trusted set). Moving one of these is a
// behavior change: it stops prompting in workspace mode.
var outsideTrustedOrigin = map[string]string{
	"generate_image":    "decided: model-initiated spend against the imagegen backend with no natural bound — the prompt is the budget; a user who wants it quiet has 'always allow'",
	"terva_restart":     "decided: restarting the process always deserves a human speed bump",
	"terva_arm_restart": "decided: arming a restart is the same act, delayed",
}

func toolUniverse(t *testing.T) map[string]bool {
	t.Helper()
	home := testsupport.TempDir(t)
	t.Setenv("TERVA_HOME", home)
	// ticket_store rides a further gate than its siblings: it registers only
	// where user config names a store that opens. Configure one before the
	// build below, or its builtin entry reads as stale and this test demands
	// the deletion of a live tool.
	seedConfiguredTicketStore(t, home)
	// The universe dir is a git repo AND carries a ticket store, so both
	// families of conditionally registered built-ins (worktree_*, ticket_*)
	// count as live here rather than reading as stale builtin entries.
	dir := gitRepoDir(t)
	if _, err := ticket.Init(dir, ticket.InitOptions{}); err != nil {
		t.Fatal(err)
	}
	reg := BuildToolRegistry(Args{}, core.ApprovalWorkspace, dir, nil, "", "", false, nil)
	if len(reg) < 9 {
		t.Fatalf("registry yielded only %d tools; the universe is not seeing them", len(reg))
	}
	u := map[string]bool{}
	for name := range reg {
		u[name] = true
	}
	// ticket_init registers on the INVERSE gate, so the store-bearing directory
	// above can never see it. A second registry in a git repo with no store is
	// what puts it in the universe, and without this the trusted-origin test
	// would read its builtin entry as stale and demand its deletion.
	for name := range BuildToolRegistry(Args{}, core.ApprovalWorkspace, gitRepoDir(t), nil, "", "", false, nil) {
		u[name] = true
	}
	for name := range registeredElsewhere {
		u[name] = true
	}
	return u
}

func TestEveryFirstPartyToolDeclaresItsTrustOrigin(t *testing.T) {
	universe := toolUniverse(t)
	for name := range universe {
		trusted := permissions.IsBuiltin(name)
		_, excused := outsideTrustedOrigin[name]
		switch {
		case !trusted && !excused:
			t.Errorf("first-party tool %q is neither in builtin nor on the outside-trusted-origin list — in workspace mode it will prompt as foreign, and nothing records whether that is chosen", name)
		case trusted && excused:
			t.Errorf("%q is in builtin AND on the outside-trusted-origin list — delete the stale entry", name)
		}
	}
	for name := range outsideTrustedOrigin {
		if !universe[name] {
			t.Errorf("outside-trusted-origin names %q, which no longer registers anywhere — delete it", name)
		}
	}
}

func TestBuiltinToolsNamesOnlyLiveTools(t *testing.T) {
	universe := toolUniverse(t)
	for _, name := range permissions.BuiltinNames() {
		if !universe[name] {
			t.Errorf("builtin trusts %q, which no longer registers anywhere — a stale trusted-origin entry outlives its tool", name)
		}
	}
}

// The three narrower maps only ever refine the first-party set; a name in one
// of them that is not first-party would classify a foreign tool by accident.
func TestClassificationMapsRefineBuiltinTools(t *testing.T) {
	for _, name := range permissions.ReadOnlyNames() {
		if !permissions.IsBuiltin(name) {
			t.Errorf("readOnly[%q] is not in builtin", name)
		}
	}
	for _, name := range permissions.EditToolNames() {
		if !permissions.IsBuiltin(name) {
			t.Errorf("editTools[%q] is not in builtin", name)
		}
	}
	for _, name := range permissions.InteractiveNames() {
		if !permissions.IsBuiltin(name) {
			t.Errorf("interactive[%q] is not in builtin", name)
		}
	}
}
