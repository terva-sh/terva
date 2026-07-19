package build

import (
	"io"
	"os"
	"strings"
	"testing"

	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/testsupport"
)

// captureStderr runs fn with os.Stderr redirected and returns what it wrote.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = w
	fn()
	_ = w.Close()
	os.Stderr = old
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

func TestUnrecognizedTools(t *testing.T) {
	recognized := map[string]bool{"read": true, "edit": true, "bash": true, "skill": true}
	got := unrecognizedTools([]string{"read", "edt", "bash", "skill"}, recognized)
	if len(got) != 1 || got[0] != "edt" {
		t.Fatalf("unrecognizedTools = %v, want [edt]", got)
	}
	if got := unrecognizedTools([]string{"read", "skill"}, recognized); got != nil {
		t.Errorf("all-recognized should yield nil, got %v", got)
	}
}

// TestBuildToolRegistryWarnsUnknownTools guards the silent-drop footgun: a
// --tools entry that matches no built-in tool (a typo) must warn on stderr, not
// vanish. And a name that IS a real tool added outside this filter (skill), or
// one plan mode legitimately drops (write), must NOT be reported as unknown.
func TestBuildToolRegistryWarnsUnknownTools(t *testing.T) {
	cwd := testsupport.TempDir(t)

	// Typo: read survives, edt warns and is dropped.
	var reg core.Registry
	out := captureStderr(t, func() {
		reg = BuildToolRegistry(Args{Tools: []string{"read", "edt"}}, core.ApprovalWorkspace, cwd, nil, "", "", false, nil)
	})
	if _, ok := reg["read"]; !ok {
		t.Error("read should still be registered alongside a typo'd name")
	}
	if _, ok := reg["edt"]; ok {
		t.Error("the typo'd name must not register")
	}
	if !strings.Contains(out, "edt") || !strings.Contains(out, "--tools") {
		t.Errorf("expected an unknown-tool note naming edt, got %q", out)
	}

	// skill is added to the registry by the caller AFTER this --tools filter, so
	// listing it must not read as a typo.
	out = captureStderr(t, func() {
		BuildToolRegistry(Args{Tools: []string{"read", "skill"}}, core.ApprovalWorkspace, cwd, nil, "", "", false, nil)
	})
	if strings.Contains(out, "unknown") {
		t.Errorf("--tools skill must not warn (skill is added by the caller), got %q", out)
	}

	// write is a real tool name that plan mode drops — not a typo, so no warning.
	out = captureStderr(t, func() {
		BuildToolRegistry(Args{Tools: []string{"write"}}, core.ApprovalPlan, cwd, nil, "", "", false, nil)
	})
	if strings.Contains(out, "unknown") {
		t.Errorf("a plan-dropped tool name must not warn as unknown, got %q", out)
	}
}
