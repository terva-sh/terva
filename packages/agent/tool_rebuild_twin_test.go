package agent

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// The rebuild-survivor rule — a fresh Resolve mints fresh containers, and
// everything long-lived must be re-adopted onto it — used to be implemented
// once per host. Its history is a series of shipped bugs, each one a survivor
// added to a single site.
//
// Two of the three sites are now one function, build.LiveToolSet.Rebuild, which
// rpc and acp both call. The workspace daemon keeps its own
// (workspace_session.go's rebuildTools) because it re-binds front-end channels
// and emits a prompt-rebuilt notice, neither of which exists off the daemon. So
// two implementations are left, and this guard holds them in step: every
// survivor rebuildTools carries must be carried by the shared helper too, or be
// exempted here with a reason.
//
// The extraction is deliberately NOT convention-shaped, and that is the point of
// this revision. The previous version recognised a survivor by an Adopt*/Use*
// prefix — and the survivor actually missing from rpc was the MCP re-merge,
// spelled MergeExtensionTools, which the pattern could not see. A guard that
// checks only the naming convention certifies the code that follows it. So:
// every method called ON the fresh Resolved, and every function handed a
// POINTER to it, has to be accounted for.
var rebuildExempt = map[string]string{
	"injectExtraTools":     "workspace-only: the tools it folds in are the web daemon's own, and no such tool exists under rpc or acp",
	"bindResolvedChannels": "workspace-only: the tools carrying a front-end channel are the daemon's; rpc and acp have no channel to re-bind, which is why a freshly-minted instance costs them nothing",
}

func TestTheSharedRebuildCarriesEverySurvivorTheWorkspaceDoes(t *testing.T) {
	rebuild, err := os.ReadFile("workspace/workspace_session.go")
	if err != nil {
		t.Fatalf("read workspace_session.go: %v", err)
	}
	shared, err := os.ReadFile("build/toolrebuild.go")
	if err != nil {
		t.Fatalf("read build/toolrebuild.go: %v", err)
	}

	fn := funcBody(t, string(rebuild), "func (s *wsSession) rebuildTools(")

	// Everything done TO the fresh Resolved: a method on it, or a function
	// handed its address.
	survivors := map[string]bool{}
	for _, m := range regexp.MustCompile(`\brr\.(\w+)\(`).FindAllStringSubmatch(fn, -1) {
		survivors[m[1]] = true
	}
	for _, m := range regexp.MustCompile(`\b(\w+)\([^()]*&rr\b`).FindAllStringSubmatch(fn, -1) {
		survivors[m[1]] = true
	}
	if len(survivors) < 4 {
		t.Fatalf("saw only %d survivors in rebuildTools (%v); the extraction is not seeing them", len(survivors), survivors)
	}

	helper := funcBody(t, string(shared), "func (s LiveToolSet) Rebuild(")
	for name := range survivors {
		inShared := strings.Contains(helper, "r."+name+"(")
		reason, exempt := rebuildExempt[name]
		switch {
		case !inShared && !exempt:
			t.Errorf("rebuildTools carries %s onto the fresh Resolved and build.LiveToolSet.Rebuild does not — "+
				"every rebuild-survivor bug so far shipped exactly this way (carry it, or exempt it with a reason)", name)
		case inShared && exempt:
			t.Errorf("%s is exempted (%q) but the shared helper now carries it — delete the stale exemption", name, reason)
		}
	}
	for name := range rebuildExempt {
		if !survivors[name] {
			t.Errorf("exemption names %s, which rebuildTools no longer does to the fresh Resolved — delete it", name)
		}
	}
}

// The other half of the same rule: a host must not hand-roll a third copy. Both
// fixed-shape hosts go through the shared helper, and neither swaps a tool set
// onto its agent by itself — which is the move that would put the survivor list
// back into three places.
func TestTheFixedShapeHostsRebuildThroughTheSharedHelper(t *testing.T) {
	for _, file := range []string{"rpc.go", "acp_mode.go"} {
		b, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}
		src := string(b)
		if !strings.Contains(src, "build.LiveToolSet{") {
			t.Errorf("%s does not build a build.LiveToolSet — if this host stopped rebuilding its tool set, say so here; "+
				"if it grew its own rebuild, that is the third copy this guard exists to prevent", file)
		}
		if strings.Contains(src, ".SetTools(") {
			t.Errorf("%s swaps a tool set onto its agent directly; the survivor list belongs in build.LiveToolSet, "+
				"not in a host", file)
		}
	}
}

// funcBody returns the source of the function whose declaration starts with
// decl, up to the next top-level func.
func funcBody(t *testing.T, src, decl string) string {
	t.Helper()
	start := strings.Index(src, decl)
	if start < 0 {
		t.Fatalf("%q not found — the function this guard reads has moved or been renamed", decl)
	}
	end := strings.Index(src[start+len(decl):], "\nfunc ")
	if end < 0 {
		return src[start:]
	}
	return src[start : start+len(decl)+end]
}
