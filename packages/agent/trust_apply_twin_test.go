package agent

import (
	"os"
	"strings"
	"testing"
)

// A trust flip has to reach four surfaces — the hook engine, the extension
// gate, the model's tool set, and its per-turn lore — and forgetting one is
// silent in both directions: nothing errors, the confirmation still prints, and
// the session simply keeps running the launch answer for whichever half was
// missed. ACP shipped a /trust verb that moved only the extension manager, and
// each of the other three read as working code.
//
// build.TrustSurfaces is that list, so the list has one home. This guard is the
// other half: a host must reach it through ApplyTrust rather than performing the
// steps itself, because a host that spells them out is a host that can spell out
// three of four.
//
// It reads sources rather than calling anything, which is the same trade
// tool_rebuild_twin_test.go makes and for the same reason — the applies are
// closures over locals of functions that spawn subprocesses before line one.

// trustHosts are the files that apply a trust flip. Adding a host means adding
// it here; the emptiness check below is what makes that unavoidable.
var trustHosts = []string{"acp_mode.go", "workspace/workspace.go", "workspace/workspace_session.go"}

func TestEveryTrustFlipGoesThroughTheSharedEvent(t *testing.T) {
	for _, file := range trustHosts {
		b, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}
		src := string(b)
		if !strings.Contains(src, "build.ApplyTrust(") && !strings.Contains(src, "ApplyTrust(ctx") {
			t.Errorf("%s is listed as a host that applies a trust flip but never calls ApplyTrust — either it "+
				"stopped applying trust (drop it from trustHosts) or it grew its own sequence, which is how ACP "+
				"came to move one surface out of four", file)
		}
	}
}

// The daemon is the one host whose surfaces are split across two scopes: one
// hook engine behind every session, and an extension manager per session. So it
// makes two calls, and the workspace-scoped one has to come FIRST — otherwise
// the withdrawal ordering ApplyTrust encodes is undone by the caller, with the
// hooks landing after a per-session reload that can take seconds.
//
// This is a source-order check because the thing it protects is a race window:
// asserting it by running the flip and timing a concurrent tool call would be
// exactly the flaky test that teaches people to re-run CI.
func TestTheDaemonMovesItsWorkspaceScopedSurfaceFirst(t *testing.T) {
	b, err := os.ReadFile("workspace/workspace.go")
	if err != nil {
		t.Fatalf("read workspace.go: %v", err)
	}
	src := string(b)
	start := strings.Index(src, "func (w *Workspace) applyTrust(")
	if start < 0 {
		t.Fatal("applyTrust not found — the function this guard reads has moved or been renamed")
	}
	body := src[start:]
	if end := strings.Index(body[1:], "\nfunc "); end >= 0 {
		body = body[:end+1]
	}
	shared := strings.Index(body, "build.ApplyTrust(")
	perSession := strings.Index(body, "setTrusted(")
	switch {
	case shared < 0:
		t.Error("applyTrust no longer calls build.ApplyTrust for the workspace-scoped surfaces (the hook engine)")
	case perSession < 0:
		t.Error("applyTrust no longer fans out to its sessions — if that moved, this ordering guard needs rewriting")
	case shared > perSession:
		t.Error("applyTrust flips its sessions BEFORE the workspace-scoped hook engine, which puts the hooks last " +
			"again: for the length of a session's extension reload, an untrusted repo's pre-tool-use hook is still " +
			"in the chain for any tool call a running turn makes")
	}
}

// The third half of this rule — that no host performs one of ApplyTrust's steps
// on its own — lives in host_census_test.go, as
// TestNoTrustStepHappensOutsideTheSharedEvent.
//
// It moved because the version here checked eight files BY NAME, and the files
// it did not name were every host nobody had examined: the SDK, bot mode, the
// deliberation clerks. A guard against forgetting a host cannot itself hold a
// list of the hosts somebody remembered.
