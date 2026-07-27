package workspace

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/testsupport"
)

// The residue is closed: a newly trusted repo's hooks fire on the NEXT TOOL
// CALL, not the next launch.
//
// This replaces TestProjectHooksDoNotRewireOnALiveTrustFlipYet, which pinned
// the gap by asserting the engine pointer was unchanged across a flip. That
// assertion would still hold now — the fix swaps the engine's SPECS rather than
// the engine — which is exactly why it had to be replaced rather than deleted:
// a pointer-identity proxy cannot tell "not re-derived" from "re-derived in
// place", and only a behavioural test can say which one shipped.
//
// Project hooks are a trust-gated execution path, so the direction that matters
// is both ways: trusting must start them, and untrusting must stop them.

// projectDenyHook writes a project config whose pre-tool-use hook denies every
// call by exiting 2 — the shell-ergonomic deny spelling the ladder honours.
func projectDenyHook(t *testing.T, cwd string) {
	t.Helper()
	dir := filepath.Join(cwd, ".terva")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir project dir: %v", err)
	}
	// `false` exits 1, not 2, so it would read as "no opinion". sh -c is the
	// portable way to get a deliberate exit 2.
	cfg := map[string]any{
		"hooks": map[string]any{
			"pre_tool_use": []map[string]any{{
				"command": "sh",
				"args":    []string{"-c", "echo 'denied by the project hook' >&2; exit 2"},
			}},
		},
	}
	b, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal project config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), b, 0o644); err != nil {
		t.Fatalf("write project config: %v", err)
	}
}

// hookVerdict runs the workspace's own pre-hook chain the way the tool-call
// ladder does, and reports whether the call was denied and why.
func hookVerdict(t *testing.T, w *Workspace) (denied bool, reason string) {
	t.Helper()
	if w.hookEng == nil {
		t.Fatal("the workspace built no hook engine — with project hooks on disk it must keep one " +
			"standing by, or trusting the repo has nowhere to put them")
	}
	res := w.hookEng.RunPre(context.Background(), "bash", json.RawMessage(`{"command":"echo hi"}`))
	return res.Decision == "deny", res.Reason
}

func trustSessionWithProjectHook(t *testing.T) *Workspace {
	t.Helper()
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	t.Setenv("OPENAI_API_KEY", "test-key")

	cwd := testsupport.TempDir(t)
	projectDenyHook(t, cwd)

	w, err := NewWorkspace(build.Args{
		Provider: "openai", Model: "gpt-5", CWD: cwd, NoExt: true, NoMCP: true,
	}, "test")
	if err != nil {
		t.Fatalf("NewWorkspace: %v", err)
	}
	t.Cleanup(func() { w.Close() })
	if _, err := w.CreateSession(context.Background(), ctrlproto.CreateOpts{}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	return w
}

func TestATrustFlipStartsAndStopsProjectHooks(t *testing.T) {
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("needs a POSIX shell to run a hook program")
	}
	w := trustSessionWithProjectHook(t)
	ctx := context.Background()

	// Untrusted: the hook is on disk and must NOT run. This is the security
	// direction — a repo you have not trusted does not get to execute a program
	// on every tool call.
	if denied, reason := hookVerdict(t, w); denied {
		t.Fatalf("an UNTRUSTED project's hook ran and denied the call (%q) — trust is not gating execution", reason)
	}

	if err := w.Trust(ctx, false); err != nil {
		t.Fatalf("Trust: %v", err)
	}
	denied, reason := hookVerdict(t, w)
	if !denied {
		t.Error("after Trust the project's pre-tool-use hook still does not run — a newly trusted " +
			"repo's hooks would wait for the next launch, which is the residue this closed")
	}
	if denied && reason == "" {
		t.Error("the deny carried no reason; the hook's stderr is what tells the model why it was refused")
	}

	if err := w.Untrust(ctx); err != nil {
		t.Fatalf("Untrust: %v", err)
	}
	if denied, _ := hookVerdict(t, w); denied {
		t.Error("after Untrust the project's hook is still running — revoking trust must stop executing " +
			"the repo's programs, or /untrust is advisory where it matters most")
	}
}

// A workspace whose project has no hooks at all keeps no engine: an engine
// whose chains can only ever be empty is a log file opened for nothing. This
// pins the narrow-ness of the standing-engine rule added for the flip.
func TestNoHooksAnywhereKeepsNoEngine(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	t.Setenv("OPENAI_API_KEY", "test-key")

	w, err := NewWorkspace(build.Args{
		Provider: "openai", Model: "gpt-5", CWD: testsupport.TempDir(t), NoExt: true, NoMCP: true,
	}, "test")
	if err != nil {
		t.Fatalf("NewWorkspace: %v", err)
	}
	t.Cleanup(func() { w.Close() })

	if w.hookEng != nil {
		t.Error("no user hooks and no project hooks, yet an engine was built — a trust flip could " +
			"never give it anything to run")
	}
	// And flipping trust against a nil engine must not panic: SetSpecs is
	// nil-safe precisely so applyTrust needs no special case.
	if err := w.Trust(context.Background(), false); err != nil {
		t.Fatalf("Trust: %v", err)
	}
}

var _ = config.ProjectHooksOnDisk
