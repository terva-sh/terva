package extensions

import (
	"testing"

	"terva.sh/terva/packages/testsupport"
)

// TestAdoptRejectsTraversalNames pins the path-traversal guard: adopt_extensions
// is untrusted project config, so a crafted "../../evil", a separator, or an
// absolute path must never become an adopt target (it would escape the global
// extensions root when filepath.Join'd in Discover). Only bare names survive.
func TestAdoptRejectsTraversalNames(t *testing.T) {
	mgr := New(testsupport.TempDir(t), testsupport.TempDir(t), "0.0.0-test", "anthropic", "claude-opus-4-7", &stubHooks{})
	mgr.SetProjectTrusted(true)
	mgr.SetAdopt("/global/extensions", []string{
		"weather",      // ok — bare name
		"../../../etc", // traversal
		"sub/evil",     // separator
		"/abs/evil",    // absolute
		"..",           // parent
		".",            // self
		"",             // empty
	})
	root, names := mgr.adoptTargets()
	if root != "/global/extensions" {
		t.Fatalf("root = %q, want /global/extensions", root)
	}
	if len(names) != 1 || names[0] != "weather" {
		t.Errorf("only the bare name should survive the guard, got %v", names)
	}
}

func TestIsPlainExtensionName(t *testing.T) {
	for _, n := range []string{"weather", "my-ext", "ext_2", "Ext.Name"} {
		if !isPlainExtensionName(n) {
			t.Errorf("%q should be accepted", n)
		}
	}
	for _, n := range []string{"", ".", "..", "../x", "a/b", "/abs", `a\b`, "sub/"} {
		if isPlainExtensionName(n) {
			t.Errorf("%q should be rejected", n)
		}
	}
}

// TestAdoptTargetsTrustGated pins the security-critical rule: adopted global
// extensions are project-declared, so they obey the SAME trust gate as the
// project's own — nothing is adopted until the project is trusted.
func TestAdoptTargetsTrustGated(t *testing.T) {
	mgr := New(testsupport.TempDir(t), testsupport.TempDir(t), "0.0.0-test", "anthropic", "claude-opus-4-7", &stubHooks{})
	mgr.SetAdopt("/global/extensions", []string{"weather", "", "weather"}) // empty + dup tolerated

	// Untrusted (the default): adopt nothing, even with names configured.
	if root, names := mgr.adoptTargets(); root != "" || len(names) != 0 {
		t.Fatalf("untrusted project must not adopt: root=%q names=%v", root, names)
	}

	// Trusted: the configured globals become adoptable (deduped, blanks dropped).
	mgr.SetProjectTrusted(true)
	root, names := mgr.adoptTargets()
	if root != "/global/extensions" {
		t.Errorf("root = %q, want /global/extensions", root)
	}
	if len(names) != 1 || names[0] != "weather" {
		t.Errorf("names = %v, want [weather]", names)
	}

	// An empty adopt set turns it off again.
	mgr.SetAdopt("/global/extensions", nil)
	if root, names := mgr.adoptTargets(); root != "" || len(names) != 0 {
		t.Errorf("empty adopt set must disable adoption: root=%q names=%v", root, names)
	}
}
