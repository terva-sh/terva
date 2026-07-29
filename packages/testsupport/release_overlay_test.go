package testsupport

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// Every docs/ entry is published or held back BY DECISION.
//
// scripts/release.sh materializes the public tree from every tracked file minus
// an EXCLUDES list, and scrubs it against a BLOCKLIST of internal hostnames. The
// scrub is what catches a leaked host string; nothing catches a whole new
// private directory that happens not to mention one. So `docs/postmortems/`, or
// a top-level note written for the maintainer, ships on the next release unless
// somebody remembers to add it to a shell array — and a release is not
// reversible, because it is a push to a public mirror.
//
// This is the self-enrolling-set pattern the repo uses elsewhere (the verb
// doc-coverage gate, the tool trust-origin set, the InteractiveConfig liveness
// walk): a new entry fails on the commit that adds it, naming the decision it
// needs, rather than joining a list nobody reads.
//
// The lists below are that decision, written down. Held-back entries are also
// JOINED to release.sh's own array — a doc declared private here but absent from
// the script would read as covered while shipping every time.

// docsHeldBack is every docs/ entry the overlay strips, with the reason. Each
// must actually appear in release.sh's EXCLUDES; a claim here that the script
// does not honour is the failure this guard exists to prevent.
var docsHeldBack = map[string]string{
	"docs/architecture":          "internal architecture notes: names internal hosts, unshipped plans, and the fork's private reasoning",
	"docs/decisions":             "decision records — the maintainer's working argument, not user documentation",
	"docs/plans":                 "work plans for unshipped features; reads as a roadmap commitment the fork does not make",
	"docs/proposals":             "design proposals, many never built and several superseded — shipping them documents things that do not exist",
	"docs/reviews":               "code/architecture review write-ups, including findings about unfixed defects",
	"docs/vanity":                "the vanity-site source, deployed separately by scripts/site.sh rather than shipped in the tree",
	"docs/working-agreements.md": "how the maintainer and the agent work together in this repo; meaningless outside it",
}

// docsThatShip is every docs/ entry that IS public. A snapshot on purpose: it is
// what makes a NEW file fail here instead of shipping unexamined. Adding a doc
// means adding a line, which is the moment to ask whether it is public.
var docsThatShip = []string{
	"docs/README.md",
	"docs/cli.md",
	"docs/connectors.md",
	"docs/context-construction.md",
	"docs/controllers.md",
	"docs/debugging-prompts.md",
	"docs/deploy.md",
	"docs/extensions.md",
	"docs/fork.md",
	"docs/hooks.md",
	"docs/image-generation.md",
	"docs/localization.md",
	"docs/mcp.md",
	"docs/models.md",
	"docs/native-image-output.md",
	"docs/permissions.md",
	"docs/personas.md",
	"docs/positioning.md",
	"docs/profiling.md",
	"docs/providers.md",
	"docs/raati.md",
	"docs/resource-limits.md",
	"docs/rpc.md",
	"docs/scripting.md",
	"docs/skills.md",
	"docs/standard-tools.md",
	"docs/themes.md",
	"docs/tui.md",
	"docs/web.md",
	"docs/workflows.md",
}

// repoRoot is this package's path back to the checkout root.
const repoRoot = "../.."

// releaseExcludes parses the EXCLUDES array out of scripts/release.sh — the
// script is the authority on what the public tree omits, so the guard reads it
// rather than restating it. Comments and blank lines inside the array are
// skipped; entries are the quoted strings.
func releaseExcludes(t *testing.T) map[string]bool {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(repoRoot, "scripts/release.sh"))
	if err != nil {
		t.Fatalf("read release.sh: %v", err)
	}
	quoted := regexp.MustCompile(`"([^"]+)"`)
	out := map[string]bool{}
	inArray := false
	for _, line := range strings.Split(string(body), "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case trimmed == "EXCLUDES=(":
			inArray = true
			continue
		case inArray && trimmed == ")":
			inArray = false
		}
		if !inArray || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if m := quoted.FindStringSubmatch(trimmed); m != nil {
			out[m[1]] = true
		}
	}
	if len(out) == 0 {
		t.Fatal("parsed no EXCLUDES entries from release.sh — the array moved or changed shape, " +
			"and an empty result would pass every check below vacuously")
	}
	return out
}

// requireSourceTree skips this file's guards when they are running inside the
// PUBLISHED projection rather than the source tree.
//
// The guards audit an enrolment decision — which docs/ entries reach the public
// mirror — and that decision has already been executed by the time the public
// tree exists: `scripts/release.sh` is itself stripped, and so is every
// held-back directory. So in the published tree there is no array to join
// against and nothing left to enrol, and the same assertions that pass here fail
// there. That is not hypothetical: this file shipped without the check and broke
// the release-verify build of the very tree it was written to protect.
//
// It fails LOUDLY on the one shape a plain skip would hide — release.sh gone
// while the private docs are still present, i.e. the script was renamed or
// moved. Skipping there would silently retire the guard everywhere.
func requireSourceTree(t *testing.T) {
	t.Helper()
	_, relErr := os.Stat(filepath.Join(repoRoot, "scripts", "release.sh"))
	if relErr == nil {
		return
	}
	if _, privErr := os.Stat(filepath.Join(repoRoot, "docs", "plans")); privErr == nil {
		t.Fatal("scripts/release.sh is missing from a tree that still carries docs/plans — the release script " +
			"was renamed or moved, and these guards can no longer read the EXCLUDES array they join against")
	}
	t.Skip("published tree: release.sh and the held-back docs are both stripped, so the enrolment this audits " +
		"has already happened and there is nothing here to check")
}

// docsEntries lists docs/ one level deep: directories and top-level files, the
// same granularity EXCLUDES uses.
func docsEntries(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(repoRoot, "docs"))
	if err != nil {
		t.Fatalf("read docs/: %v", err)
	}
	var out []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") {
			continue
		}
		out = append(out, "docs/"+e.Name())
	}
	if len(out) < 10 {
		t.Fatalf("found only %d docs entries; the walk is not seeing them", len(out))
	}
	sort.Strings(out)
	return out
}

func TestEveryDocsEntryIsPublishedOrHeldBackByDecision(t *testing.T) {
	requireSourceTree(t)
	ships := map[string]bool{}
	for _, p := range docsThatShip {
		ships[p] = true
	}

	var unclassified, both []string
	for _, entry := range docsEntries(t) {
		_, held := docsHeldBack[entry]
		switch {
		case !held && !ships[entry]:
			unclassified = append(unclassified, entry)
		case held && ships[entry]:
			both = append(both, entry)
		}
	}
	if len(unclassified) > 0 {
		t.Errorf("docs entries classified neither public nor held back:\n  %s\n"+
			"Every docs/ entry ships to the public mirror unless release.sh excludes it, and a release "+
			"cannot be taken back. Add each to docsThatShip, or to docsHeldBack WITH a reason and to "+
			"release.sh's EXCLUDES.", strings.Join(unclassified, "\n  "))
	}
	if len(both) > 0 {
		t.Errorf("docs entries on both lists — delete the wrong one: %s", strings.Join(both, ", "))
	}
}

// TestHeldBackDocsAreActuallyExcluded is the join, and the reason this guard is
// worth more than a comment: a doc can be declared private here and still ship,
// because the shell array is what the release actually reads.
func TestHeldBackDocsAreActuallyExcluded(t *testing.T) {
	requireSourceTree(t)
	excluded := releaseExcludes(t)
	for entry, why := range docsHeldBack {
		if !excluded[entry] {
			t.Errorf("%s is held back here (%q) but is NOT in release.sh's EXCLUDES — "+
				"it ships on the next release", entry, why)
		}
	}
}

// TestNoStaleDocsClassification: a list that outlives the file it describes
// stops being a record and starts being noise, and the next reader trusts it.
func TestNoStaleDocsClassification(t *testing.T) {
	requireSourceTree(t)
	live := map[string]bool{}
	for _, e := range docsEntries(t) {
		live[e] = true
	}
	for entry := range docsHeldBack {
		if !live[entry] {
			t.Errorf("docsHeldBack names %s, which no longer exists — delete the entry", entry)
		}
	}
	for _, entry := range docsThatShip {
		if !live[entry] {
			t.Errorf("docsThatShip names %s, which no longer exists — delete the entry", entry)
		}
	}
}

// TestEveryHeldBackDocGivesAReason: the entry IS the decision record, so a blank
// or placeholder reason defeats the point.
func TestEveryHeldBackDocGivesAReason(t *testing.T) {
	requireSourceTree(t)
	for entry, why := range docsHeldBack {
		if len(strings.TrimSpace(why)) < 20 {
			t.Errorf("%s is held back with no real reason (%q) — say what makes it private", entry, why)
		}
	}
}

// ---- the EXCLUDES array itself ----
//
// The checks above join docs/ to EXCLUDES. These two join EXCLUDES to the
// REPOSITORY, which is the direction that has actually cost a release.
//
// An EXCLUDES entry is a PATH STRING, and a rename invalidates it in total
// silence: the array still parses, the release still builds, and the thing that
// was private now ships. That is not a hypothetical — the same failure in
// .gitattributes moved the embedded personas out from under their line-ending
// pin, and it surfaced as seven charter tests failing on a Windows runner with a
// message about a missing paragraph. A release is a push to a public mirror, so
// the equivalent here has no recovery at all.

// excludesNamingNothingTracked are EXCLUDES entries that deliberately name a
// path the repository does not track, with the reason. The overlay is built from
// TRACKED files, so an entry like this strips nothing; it is a closed door with a
// second lock. Fine to keep — but say so, or it is indistinguishable from an
// entry left behind by a rename, which is the failure these guards exist to catch.
var excludesNamingNothingTracked = map[string]string{
	"web-tools-evaluation-report.md": "gitignored by /*-report.md, so it can never be committed and never reaches the overlay; the entry is belt-and-braces against the ignore rule changing",
}

// trackedPaths is every path git tracks. Tracked-ness — not presence on disk —
// is the right question: the overlay is built from the tracked set, and an
// untracked local artifact is present on one machine and absent on another,
// which would make a disk check fail for some developers and not others.
func trackedPaths(t *testing.T) []string {
	t.Helper()
	out, err := exec.Command("git", "-C", repoRoot, "ls-files", "-z").Output()
	if err != nil {
		t.Fatalf("git ls-files: %v (these guards read the tracked set; without it they cannot check anything)", err)
	}
	var paths []string
	for _, p := range strings.Split(string(out), "\x00") {
		if p != "" {
			paths = append(paths, p)
		}
	}
	if len(paths) < 100 {
		t.Fatalf("git tracks only %d paths; the listing is not seeing them and every check below would pass vacuously", len(paths))
	}
	return paths
}

// covers reports whether an EXCLUDES entry accounts for a tracked path: the
// entry either IS the path, or is a directory prefix of it.
func covers(entry, path string) bool {
	return path == entry || strings.HasPrefix(path, entry+"/")
}

// TestEveryExcludeStillStripsSomething is the rename tripwire. An entry that
// matches no tracked path is either stale or — the dangerous reading — pointing
// at where something private USED to live, while the thing itself moved
// somewhere the overlay no longer excludes.
func TestEveryExcludeStillStripsSomething(t *testing.T) {
	requireSourceTree(t)
	tracked := trackedPaths(t)

	for entry := range releaseExcludes(t) {
		var hit bool
		for _, p := range tracked {
			if covers(entry, p) {
				hit = true
				break
			}
		}
		reason, excused := excludesNamingNothingTracked[entry]
		switch {
		case hit && excused:
			t.Errorf("EXCLUDES %q is listed as naming nothing tracked (%q), but it now strips real files — "+
				"delete the stale excuse", entry, reason)
		case !hit && !excused:
			t.Errorf("EXCLUDES %q matches no tracked path. Either it is stale, or whatever it kept private "+
				"was RENAMED and is now shipping to the public mirror under its new path. Point the entry at "+
				"the new one, or record here why it strips nothing.", entry)
		case !hit && excused && len(strings.TrimSpace(reason)) < 20:
			t.Errorf("EXCLUDES %q is excused with no real reason (%q)", entry, reason)
		}
	}
}

// rootThatShips is every tracked top-level entry that IS public. A snapshot, for
// the same reason docsThatShip is one: it makes a NEW root entry fail here
// instead of shipping unexamined. The root is where a maintainer note or a
// scratch directory lands first, and there are only ~30 of them.
var rootThatShips = []string{
	".gitattributes", ".gitignore", ".goreleaser.yaml", "CHANGELOG.md", "Dockerfile",
	"LICENSE", "README.md", "assets", "cmd", "docs", "docs.go", "docs_test.go", "e2e",
	"examples", "go.mod", "go.sum", "install.ps1", "install.sh", "justfile", "packages",
	"scripts", "testdata",
}

// TestEveryRootEntryIsPublishedOrExcluded generalises the docs/ check to the
// place a new private thing is most likely to appear. docs/ was covered because
// that is where the leak was noticed; nothing covered the root, where AGENTS.md,
// experiments/ and two .just files already live behind EXCLUDES.
func TestEveryRootEntryIsPublishedOrExcluded(t *testing.T) {
	requireSourceTree(t)
	excludes := releaseExcludes(t)
	ships := map[string]bool{}
	for _, p := range rootThatShips {
		ships[p] = true
	}

	seen := map[string]bool{}
	var unclassified, both []string
	for _, p := range trackedPaths(t) {
		root := p
		if i := strings.IndexByte(p, '/'); i >= 0 {
			root = p[:i]
		}
		if seen[root] {
			continue
		}
		seen[root] = true
		// An entry is excluded if EXCLUDES names it, or names a parent of it.
		excluded := false
		for e := range excludes {
			if covers(e, root) {
				excluded = true
				break
			}
		}
		switch {
		case !excluded && !ships[root]:
			unclassified = append(unclassified, root)
		case excluded && ships[root]:
			both = append(both, root)
		}
	}
	sort.Strings(unclassified)
	if len(unclassified) > 0 {
		t.Errorf("tracked top-level entries classified neither public nor excluded:\n  %s\n"+
			"Everything tracked ships to the public mirror unless release.sh excludes it, and a release "+
			"cannot be taken back. Add each to rootThatShips, or to release.sh's EXCLUDES.",
			strings.Join(unclassified, "\n  "))
	}
	if len(both) > 0 {
		sort.Strings(both)
		t.Errorf("root entries both excluded and listed as shipping — delete the wrong one: %s",
			strings.Join(both, ", "))
	}
	for _, p := range rootThatShips {
		if !seen[p] {
			t.Errorf("rootThatShips names %s, which git no longer tracks — delete the entry", p)
		}
	}
}
