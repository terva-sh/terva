package testsupport

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// terva has no global egress allowlist, and that is a design decision rather
// than an omission. A single list cannot say which fetch it applies to, so one
// entry would exempt a host for every guard in the process. Instead each call
// site builds its own guard from the configuration that names its own
// destination: the MCP transport allowlists its configured server host, and
// the pack fetcher allowlists the hosts named in pack_registries.
//
// The decision is only worth as much as its enforcement. These two gates fail
// when a new caller appears, so widening the pattern becomes a deliberate act
// with a reason attached rather than a quiet import.

// goFilesContaining returns every non-test Go file holding needle, as
// repo-relative slash paths.
func goFilesContaining(t *testing.T, needle string) []string {
	t.Helper()
	var found []string
	err := filepath.WalkDir(repoRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		// Sibling worktrees live under this root. Without SkipScanDir the
		// walk reports another branch's files as callers in this tree.
		if SkipScanDir(repoRoot, path, d) {
			return fs.SkipDir
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, rerr := filepath.Rel(repoRoot, path)
		if rerr != nil {
			return rerr
		}
		src, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		if strings.Contains(string(src), needle) {
			found = append(found, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk the tree for %q: %v", needle, err)
	}
	sort.Strings(found)
	return found
}

// checkCallers fails when the files holding needle differ from the expected
// set, naming both directions so the failure says what to do.
func checkCallers(t *testing.T, needle string, expected map[string]string, why string) {
	t.Helper()
	got := goFilesContaining(t, needle)
	seen := map[string]bool{}
	for _, f := range got {
		seen[f] = true
		if _, ok := expected[f]; !ok {
			t.Errorf("%s references %s and is not a known call site.\n%s\nAdd it to the map in this test with the reason it needs its own guard, or route it through an existing call site.", f, needle, why)
		}
	}
	for f, reason := range expected {
		if !seen[f] {
			t.Errorf("%s no longer references %s, but this test still expects it to (%s). Remove the entry, or restore the call site.", f, needle, reason)
		}
	}
}

// packRegistryCallers: pack_registries feeds exactly one fetch. If a second
// file reads it, the key has become the global allowlist this design rejected.
var packRegistryCallers = map[string]string{
	"packages/agent/config/config.go": "declares the field",
	"packages/agent/extpack.go":       "the only consumer, building the pack fetch's own guard",

	// These two read the list without granting anything with it. Reading is
	// fine; a second file that turned an entry into an egress exemption is
	// what this gate is here to catch.
	"packages/agent/modes/status_view.go":   "displays the list in /status, so the exemption is visible",
	"packages/agent/build/configsecrets.go": "refuses the config read-lift when an entry carries userinfo",
}

func TestPackRegistriesFeedsOnlyThePackFetch(t *testing.T) {
	checkCallers(t, "PackRegistries", packRegistryCallers,
		"pack_registries exempts a host from the egress guard's address policy for ONE fetch. A second reader turns it into a global allowlist, which is the shape TKT-01M266NA44 rejected.")
}

// egressAllowHostCallers: allowlisting is per call site, from the config that
// names that call site's destination.
var egressAllowHostCallers = map[string]string{
	"packages/agent/mcp/mcp_http.go": "allowlists the MCP server's own configured host",
	"packages/agent/extpack.go":      "allowlists each configured pack registry host",
}

func TestEgressAllowlistingStaysPerCallSite(t *testing.T) {
	checkCallers(t, "egress.AllowHost", egressAllowHostCallers,
		"An allowlist entry must come from the configuration naming that destination, so the grant dies when the destination does. A new caller is a real decision: say what config names its host.")
}
