package worktree

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"terva.sh/terva/packages/testsupport"
)

// legacyEnv builds an Env with both roots set, and lays a worktree down the way
// the retired terva-git-worktree extension did: checkout under
// <legacyRoot>/<key>/worktrees/<name>, registry.json (no path field) next to
// it. Returns the env and the legacy checkout path.
func legacyEnv(t *testing.T, repoDir string, session string) (Env, string) {
	t.Helper()
	newRoot := testsupport.TempDir(t)
	legacyRoot := testsupport.TempDir(t)
	e := Env{Root: newRoot, LegacyRoot: legacyRoot, CWD: repoDir, SessionID: session}

	// Create through an env rooted at the LEGACY dir — byte-for-byte what the
	// extension produced (its registry had no "path" field; strip it).
	m := fixedManager(true)
	res, err := m.Create(Env{Root: legacyRoot, CWD: repoDir, SessionID: "ext-era"}, CreateArgs{Name: "legacy-wt", ReuseIfAvailable: true})
	if err != nil {
		t.Fatal(err)
	}
	r, err := resolveRepo(e)
	if err != nil {
		t.Fatal(err)
	}
	legacyRegPath := filepath.Join(legacyRoot, r.key, "registry.json")
	b, err := os.ReadFile(legacyRegPath)
	if err != nil {
		t.Fatal(err)
	}
	var reg map[string]map[string]map[string]any
	if err := json.Unmarshal(b, &reg); err != nil {
		t.Fatal(err)
	}
	for _, e := range reg["worktrees"] {
		delete(e, "path")
	}
	b, _ = json.MarshalIndent(reg, "", "  ")
	if err := os.WriteFile(legacyRegPath, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return e, res.Path
}

// A repo whose registry exists only in the extension's data dir is adopted on
// first touch: the entry appears with its LEGACY checkout path pinned (the
// checkout never moves — git records absolute paths in .git/worktrees), and
// the merged registry now lives under the first-class root.
func TestMigrationAdoptsLegacyRegistryWithPinnedPaths(t *testing.T) {
	repoDir, _ := newRepo(t)
	e, legacyPath := legacyEnv(t, repoDir, "sess-new")
	m := fixedManager(false) // ext-era pid reads dead => claim stale, not held

	lst, err := m.List(e, ListFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(lst.Worktrees) != 1 {
		t.Fatalf("want the legacy worktree listed, got %d items", len(lst.Worktrees))
	}
	it := lst.Worktrees[0]
	if it.Name != "legacy-wt" || canonPath(it.Path) != canonPath(legacyPath) {
		t.Errorf("legacy worktree should keep its extension-era path: %+v (want %s)", it, legacyPath)
	}
	if it.Unmanaged {
		t.Error("a migrated worktree is managed, not unmanaged")
	}

	// The migration persisted to the new home, with the path pinned.
	r, err := resolveRepo(e)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(r.registryPath())
	if err != nil {
		t.Fatalf("migrated registry missing from the first-class home: %v", err)
	}
	// The registry is JSON; on Windows the pinned path's separators are escaped
	// (\ -> \\), so match the JSON-escaped form rather than the raw path.
	esc, _ := json.Marshal(legacyPath)
	if !strings.Contains(string(b), string(esc[1:len(esc)-1])) {
		t.Errorf("migrated registry does not pin the legacy path:\n%s", b)
	}
}

// Operations work on a migrated worktree at its legacy location: claim it,
// then remove it (force, since fixedManager treats the checkout as clean
// anyway) — both resolve the pinned path, not a derived new-home one.
func TestMigrationClaimAndRemoveAtLegacyPath(t *testing.T) {
	repoDir, _ := newRepo(t)
	e, legacyPath := legacyEnv(t, repoDir, "sess-new")
	m := fixedManager(false)

	cl, err := m.Claim(e, ClaimArgs{Name: "legacy-wt"})
	if err != nil {
		t.Fatal(err)
	}
	if canonPath(cl.Path) != canonPath(legacyPath) || !cl.ReclaimedStale {
		t.Errorf("claim should hand over the stale legacy worktree in place: %+v", cl)
	}

	if _, err := m.Remove(e, RemoveArgs{Name: "legacy-wt"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(legacyPath); !os.IsNotExist(err) {
		t.Errorf("remove should delete the legacy checkout, stat err = %v", err)
	}
}

// New worktrees created after migration land under the first-class root — the
// legacy dir only ever shrinks.
func TestMigrationNewCreatesLandInNewHome(t *testing.T) {
	repoDir, _ := newRepo(t)
	e, _ := legacyEnv(t, repoDir, "sess-new")
	m := fixedManager(true)

	res, err := m.Create(e, CreateArgs{Name: "fresh", ReuseIfAvailable: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(canonPath(res.Path), canonPath(e.Root)) {
		t.Errorf("new worktree should live under the first-class root %s, got %s", e.Root, res.Path)
	}

	lst, err := m.List(e, ListFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(lst.Worktrees) != 2 {
		t.Fatalf("want legacy + fresh, got %d", len(lst.Worktrees))
	}
}

// Migration happens exactly once: after the first touch, edits to the legacy
// registry are ignored (the new home is authoritative).
func TestMigrationIsOneShot(t *testing.T) {
	repoDir, _ := newRepo(t)
	e, _ := legacyEnv(t, repoDir, "sess-new")
	m := fixedManager(false)

	if _, err := m.List(e, ListFilter{}); err != nil {
		t.Fatal(err) // first touch migrates
	}
	r, err := resolveRepo(e)
	if err != nil {
		t.Fatal(err)
	}
	// Vandalize the legacy registry; a migrated repo must not read it again.
	if err := os.WriteFile(r.legacyRegistryPath(), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := m.List(e, ListFilter{}); err != nil {
		t.Errorf("post-migration ops must not consult the legacy registry: %v", err)
	}
}

// A worktree created out-of-band under the LEGACY worktrees dir still surfaces
// as unmanaged — the scan covers both homes.
func TestMigrationUnmanagedScanCoversLegacyDir(t *testing.T) {
	repoDir, _ := newRepo(t)
	e, _ := legacyEnv(t, repoDir, "sess-new")
	m := fixedManager(false)

	r, err := resolveRepo(e)
	if err != nil {
		t.Fatal(err)
	}
	stray := filepath.Join(r.legacyWorktreesDir(), "stray")
	runT(t, repoDir, "worktree", "add", "--quiet", stray, "-b", "stray-branch")

	lst, err := m.List(e, ListFilter{})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, it := range lst.Worktrees {
		if it.Name == "stray" && it.Unmanaged {
			found = true
		}
	}
	if !found {
		t.Errorf("out-of-band worktree under the legacy dir should list as unmanaged: %+v", lst.Worktrees)
	}
}
