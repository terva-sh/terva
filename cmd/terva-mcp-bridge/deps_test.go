package main

import (
	"os/exec"
	"strings"
	"testing"
)

// "The bridge takes no core dependency" is the invariant that JUSTIFIES every
// hand-rolled copy in this binary — its own JSON-RPC framing, its own SSE
// reader, its own home resolution, its own frame ceiling. Each of those is a
// deliberate duplicate paid for by this rule.
//
// Nothing enforced it. packages/agent/extensions has exactly this test for
// exactly this reason (see its deps_test.go), and the binary whose whole design
// rests on the boundary had none — so the rule held only as long as everyone
// remembered it, while the copies it pays for accumulated.
//
// It matters because a core import would pull the agent's whole world into a
// helper that ships as a standalone executable and runs inside other people's
// MCP clients: the point of the bridge is that it is small and has no opinion
// about terva's runtime.
//
// go list -deps, not the import lines: an indirect dependency three packages
// deep breaks the boundary exactly as thoroughly as a direct one, and reading
// the source would miss it.
func TestBridgeTakesNoCoreDependency(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", "terva.sh/terva/cmd/terva-mcp-bridge").Output()
	if err != nil {
		t.Fatalf("go list -deps: %v", err)
	}
	deps := strings.Split(strings.TrimSpace(string(out)), "\n")
	// Vacuity floor: a broken command yielding one line would pass every
	// assertion below without looking at anything.
	if len(deps) < 20 {
		t.Fatalf("go list -deps returned %d packages; the scan is not reading the build", len(deps))
	}

	forbidden := map[string]string{
		"terva.sh/terva/packages/core":            "the agent runtime; the bridge relays frames and runs no agent",
		"terva.sh/terva/packages/provider":        "model clients; the bridge never talks to a model",
		"terva.sh/terva/packages/agent":           "the whole host; the bridge is a standalone relay",
		"terva.sh/terva/packages/agent/build":     "agent assembly",
		"terva.sh/terva/packages/agent/config":    "the host's config layer; the bridge resolves its own home",
		"terva.sh/terva/packages/agent/mcp":       "core's MCP client — the one this binary deliberately re-implements",
		"terva.sh/terva/packages/tui":             "terminal rendering; the bridge has no UI",
		"terva.sh/terva/packages/agent/ctrlproto": "the control plane; the bridge speaks MCP, not ctrlproto",
	}
	for _, dep := range deps {
		if why, bad := forbidden[dep]; bad {
			t.Errorf("terva-mcp-bridge imports %s (%s).\n"+
				"  That boundary is what justifies this binary's hand-rolled JSON-RPC, SSE reader, home\n"+
				"  resolution and frame ceiling. Either drop the import, or delete the copies it paid for.", dep, why)
		}
	}
}

// The packages the bridge IS allowed to share, asserted so the boundary reads
// as a considered line rather than "imports nothing from terva".
//
// Without this the test above passes on a bridge that shares nothing at all,
// which would mean the small leaf packages had been copied too — the failure
// this rule is meant to bound, not cause.
func TestTheBridgeStillSharesItsLeafPackages(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", "terva.sh/terva/cmd/terva-mcp-bridge").Output()
	if err != nil {
		t.Fatalf("go list -deps: %v", err)
	}
	have := map[string]bool{}
	for _, dep := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		have[dep] = true
	}
	for _, want := range []string{
		// The bounded frame reader. The bridge reads wire frames from an
		// untrusted server; re-implementing THAT would re-introduce the
		// unbounded-line hazard lineframe exists to remove.
		"terva.sh/terva/packages/lineframe",
		// Home resolution, shared because a second copy is what silently wrote
		// OAuth tokens to a directory terva does not read.
		"terva.sh/terva/packages/envcompat",
	} {
		if !have[want] {
			t.Errorf("the bridge no longer depends on %s — a leaf package it should SHARE, not copy", want)
		}
	}
}
