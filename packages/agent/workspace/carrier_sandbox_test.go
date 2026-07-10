package workspace

import (
	"context"
	"testing"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/agent/tools"
	"terva.sh/terva/packages/testsupport"
)

// TestWorkspaceSandboxShared verifies the workspace-shared sandbox invariant
// that makes /jail work on the carrier: every session's file tools point at the
// one Workspace.Sandbox(), so locking it (what /jail does) confines that
// session's tools — and, because the TUI is handed the same pointer, its local
// !-shell escape too.
func TestWorkspaceSandboxShared(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	t.Setenv("OPENAI_API_KEY", "test-key")
	cwd := testsupport.TempDir(t)
	args := build.Args{Provider: "openai", Model: "gpt-5", CWD: cwd, NoExt: true, NoMCP: true}

	w, err := NewWorkspace(args, "test")
	if err != nil {
		t.Fatalf("NewWorkspace: %v", err)
	}
	defer w.Close()
	if w.Sandbox() == nil {
		t.Fatal("Workspace.Sandbox() is nil")
	}

	ctx := context.Background()
	info, err := w.CreateSession(ctx, ctrlproto.CreateOpts{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	// White-box: AgentFor is gone (plan 4.1), but an in-package test can reach
	// the live session's agent to inspect its resolved tool registry.
	s := w.existing(info.ID)
	if s == nil || s.agent == nil {
		t.Fatal("session did not materialize an agent")
	}
	ag := s.agent

	tool, ok := ag.LookupTool("bash")
	if !ok {
		t.Fatal("session agent has no bash tool")
	}
	bash, ok := tool.(*tools.BashTool)
	if !ok {
		t.Fatalf("bash tool is %T, want *tools.BashTool", tool)
	}
	if bash.Sandbox != w.Sandbox() {
		t.Fatal("session bash tool does not reference the workspace-shared sandbox")
	}

	// /jail (Lock) / /unjail (Unlock) drive Workspace.Sandbox(); the session's
	// tool observes both because it is the same object (independent of the
	// resolveJail startup default).
	w.Sandbox().Lock()
	if !bash.Sandbox.Locked() {
		t.Fatal("locking Workspace.Sandbox() did not jail the session's bash tool")
	}
	w.Sandbox().Unlock()
	if bash.Sandbox.Locked() {
		t.Fatal("unlocking Workspace.Sandbox() did not unjail the session's bash tool")
	}
}
