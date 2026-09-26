package core_test

import (
	"bufio"
	"os"
	"os/exec"
	"sort"
	"strings"
	"testing"
)

const (
	importModulePrefix   = "terva.sh/terva/"
	importBaselinePath   = "testdata/import_boundary_baseline.txt"
	importBaselineHeader = "<unit> <dependency> <kind> <reason>"
)

// importBoundaryUnits are the packages this test guards: the engine and the
// wire (decision 0021, rule 6).
var importBoundaryUnits = []string{"packages/core", "packages/provider"}

// TestImportBoundary holds the engine's and the wire's terva imports to a
// shrink-only baseline, in the pattern of
// packages/agent/extdriver/deps_test.go. It fails on a dependency the
// baseline does not list, and on a baseline line whose dependency is gone,
// the rule terva-ste-lint uses for its own baseline.
//
// 🔑 It reads `go list -deps`, not direct imports. I/O in a package the
// engine imports (i18n reads $TERVA_HOME) never shows in the engine's own
// files, so the I/O ban (TKT-01M35WJYT) cannot see it and this test must.
// See docs/plans/engine-extraction.md, phase 0.
func TestImportBoundary(t *testing.T) {
	baseline := readImportBaseline(t)
	for _, unit := range importBoundaryUnits {
		t.Run(unit, func(t *testing.T) {
			deps, importers := importTervaDeps(t, unit)
			want := baseline[unit]
			for _, dep := range importSortedKeys(deps) {
				if _, ok := want[dep]; ok {
					continue
				}
				t.Errorf("%s now depends on %s (imported by %s), which %s does not list.\n"+
					"Remove the import. The baseline only shrinks: add a line only for the wire or a pure leaf, never for a package that does I/O.",
					unit, dep, strings.Join(importers[dep], ", "), importBaselinePath)
			}
			for _, dep := range importSortedKeys(want) {
				if deps[dep] {
					continue
				}
				t.Errorf("%s no longer depends on %s. Delete line %d of %s:\n\t%s %s",
					unit, dep, want[dep], importBaselinePath, unit, dep)
			}
		})
	}
}

// importTervaDeps returns the terva packages unit depends on, other than itself,
// and for each one the terva packages in that set which import it.
func importTervaDeps(t *testing.T, unit string) (map[string]bool, map[string][]string) {
	t.Helper()
	out, err := exec.Command("go", "list", "-deps", "-f", `{{.ImportPath}} {{join .Imports " "}}`, importModulePrefix+unit).Output()
	if err != nil {
		t.Fatalf("go list -deps %s: %v", unit, err)
	}
	deps := map[string]bool{}
	importers := map[string][]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 || !strings.HasPrefix(fields[0], importModulePrefix) {
			continue
		}
		pkg := strings.TrimPrefix(fields[0], importModulePrefix)
		if pkg != unit {
			deps[pkg] = true
		}
		for _, imp := range fields[1:] {
			if strings.HasPrefix(imp, importModulePrefix) {
				dep := strings.TrimPrefix(imp, importModulePrefix)
				importers[dep] = append(importers[dep], pkg)
			}
		}
	}
	if len(deps) == 0 {
		// 🚨 A go list that printed nothing would pass every new-dependency
		// check. The baseline is never empty for either unit today.
		t.Fatalf("go list -deps %s reported no terva dependencies; the probe is broken", unit)
	}
	return deps, importers
}

// readImportBaseline maps unit to dependency to line number. It fails on any line that does
// not carry a known unit, a kind, and a reason.
func readImportBaseline(t *testing.T) map[string]map[string]int {
	t.Helper()
	f, err := os.Open(importBaselinePath)
	if err != nil {
		t.Fatalf("open baseline: %v", err)
	}
	defer f.Close()

	known := map[string]bool{}
	for _, u := range importBoundaryUnits {
		known[u] = true
	}
	baseline := map[string]map[string]int{}
	sc := bufio.NewScanner(f)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 4 {
			t.Errorf("%s:%d: want %s, got %q", importBaselinePath, n, importBaselineHeader, line)
			continue
		}
		unit, dep, kind := fields[0], fields[1], fields[2]
		reason := strings.Join(fields[3:], " ")
		if !known[unit] {
			t.Errorf("%s:%d: unknown unit %q; the units are %v", importBaselinePath, n, unit, importBoundaryUnits)
			continue
		}
		switch kind {
		case "wire":
			if unit != "packages/core" || dep != "packages/provider" {
				t.Errorf("%s:%d: kind wire is only the engine's dependency on packages/provider", importBaselinePath, n)
			}
		case "pure":
		case "io":
			if !strings.Contains(reason, "TKT-") {
				t.Errorf("%s:%d: an io entry must name the ticket that removes it", importBaselinePath, n)
			}
		default:
			t.Errorf("%s:%d: kind %q is not wire, pure, or io", importBaselinePath, n, kind)
		}
		if baseline[unit] == nil {
			baseline[unit] = map[string]int{}
		}
		if prev, dup := baseline[unit][dep]; dup {
			t.Errorf("%s:%d: %s %s is already listed on line %d", importBaselinePath, n, unit, dep, prev)
			continue
		}
		baseline[unit][dep] = n
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("read baseline: %v", err)
	}
	return baseline
}

func importSortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
