package agent

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// The rebuild-survivor rule — a fresh Resolve mints fresh containers, and
// everything long-lived must be re-adopted onto it — is implemented twice:
// workspace_session.go's rebuildTools (named, directly tested) and rpc.go's
// anonymous SetOnReload closure (untestable as written: it captures five
// locals of an 800-line function that spawns real subprocesses before line
// one of the closure). The rule's history is four shipped bugs, each a
// survivor added to one site and not the other.
//
// Until the closure grows a seam, this guard reads both sources and holds the
// closure to rebuildTools' survivor set: every Adopt*/Use* call rebuildTools
// makes must appear in the rpc closure, or carry a reason below. A fifth
// survivor added to the workspace side fails here until rpc answers.
var rpcReloadExempt = map[string]string{
	"UseSandbox": "rpc has no workspace-shared sandbox: its jail default is off, and no /jail UI exists to create live state worth preserving",
}

func TestRPCReloadClosureMatchesTheRebuildSurvivors(t *testing.T) {
	rebuild, err := os.ReadFile("workspace/workspace_session.go")
	if err != nil {
		t.Fatalf("read workspace_session.go: %v", err)
	}
	rpcSrc, err := os.ReadFile("rpc.go")
	if err != nil {
		t.Fatalf("read rpc.go: %v", err)
	}

	// The survivor set: Adopt*/Use* calls on the fresh Resolved inside
	// rebuildTools. The receiver there is `rr`.
	body := string(rebuild)
	start := strings.Index(body, "func (s *wsSession) rebuildTools(")
	if start < 0 {
		t.Fatal("rebuildTools not found — the twin this guard mirrors moved")
	}
	end := strings.Index(body[start:], "\nfunc ")
	if end < 0 {
		end = len(body) - start
	}
	fn := body[start : start+end]
	re := regexp.MustCompile(`rr\.((?:Adopt|Use)\w+)\(`)
	survivors := map[string]bool{}
	for _, m := range re.FindAllStringSubmatch(fn, -1) {
		survivors[m[1]] = true
	}
	if len(survivors) < 2 {
		t.Fatalf("saw only %d survivor adoptions in rebuildTools; the extraction is not seeing them", len(survivors))
	}

	// The rpc closure: everything between the SetOnReload closure's birth and
	// its registration.
	rpcBody := string(rpcSrc)
	cs := strings.Index(rpcBody, "mergeExtTools := func() {")
	ce := strings.Index(rpcBody, "extMgr.SetOnReload(mergeExtTools)")
	if cs < 0 || ce < 0 || ce < cs {
		t.Fatal("rpc.go's SetOnReload closure not found — if it grew a seam, replace this source-reading guard with a real unit test")
	}
	closure := rpcBody[cs:ce]

	for name := range survivors {
		inRPC := strings.Contains(closure, "resolved."+name+"(")
		reason, exempt := rpcReloadExempt[name]
		switch {
		case !inRPC && !exempt:
			t.Errorf("rebuildTools adopts %s but rpc.go's reload closure does not — the last four rebuild-survivor bugs shipped exactly this way (add it, or exempt it with a reason)", name)
		case inRPC && exempt:
			t.Errorf("%s is exempted (%q) but the rpc closure now carries it — delete the stale exemption", name, reason)
		}
	}
	for name := range rpcReloadExempt {
		if !survivors[name] {
			t.Errorf("exemption names %s, which rebuildTools no longer adopts — delete it", name)
		}
	}
}
