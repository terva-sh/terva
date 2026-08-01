package workspace

import (
	"testing"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/swarm"
	"terva.sh/terva/packages/agent/tools"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/testsupport"
)

// terva_status reports spend by sub-agents that are still running, which the
// session's delegated total cannot include — a child is booked against the
// parent only when its recap flushes, so "of which delegated" understates for as
// long as a swarm is in flight.
//
// The binding is made in injectExtraTools rather than in Resolve, which puts it
// on the REBUILD path: an extension policy assertion or entering plan mode mints
// a fresh StatusTool, and one that silently lost its swarm would under-report
// during exactly the window this exists for. This test runs the derivation twice
// for that reason — the second run is the rebuild.
func TestStatusToolKeepsItsSwarmAcrossARebuild(t *testing.T) {
	w, s, _ := chatTestWorkspace(t, "s1")
	root := testsupport.TempDir(t)
	w.swarm = swarm.New(swarm.Config{Root: root, RepoRoot: root})
	t.Cleanup(w.swarm.StopAll)

	derive := func() *tools.StatusTool {
		t.Helper()
		r := build.Resolved{
			ToolRegistry: core.Registry{"terva_status": &tools.StatusTool{CWD: s.cwd}},
			CWD:          s.cwd,
		}
		w.injectExtraTools(s, &r, build.Args{})
		st, _ := r.ToolRegistry["terva_status"].(*tools.StatusTool)
		return st
	}

	first := derive()
	if first == nil {
		t.Fatal("terva_status vanished from the registry")
	}
	if first.Swarm == nil {
		t.Fatal("terva_status was never given the workspace swarm")
	}
	if rebuilt := derive(); rebuilt == nil || rebuilt.Swarm == nil {
		t.Error("a rebuilt terva_status lost its swarm; in-flight delegated spend would silently vanish")
	}
}

// A workspace with no swarm must not panic or fabricate a binding — the status
// line is simply omitted, which is what every host that never wires one gets.
func TestStatusToolToleratesNoSwarm(t *testing.T) {
	w, s, _ := chatTestWorkspace(t, "s1")
	w.swarm = nil

	r := build.Resolved{
		ToolRegistry: core.Registry{"terva_status": &tools.StatusTool{CWD: s.cwd}},
		CWD:          s.cwd,
	}
	w.injectExtraTools(s, &r, build.Args{})
	st, _ := r.ToolRegistry["terva_status"].(*tools.StatusTool)
	if st == nil {
		t.Fatal("terva_status vanished from the registry")
	}
	if st.Swarm != nil {
		t.Error("a workspace with no swarm bound one anyway")
	}
}
