package privfs_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"terva.sh/terva/packages/testsupport"
)

// mkdirModeRe matches an os.MkdirAll whose mode argument is a literal, and
// captures the literal. A computed mode (a variable, privfs.DirMode) does not
// match and is not the target: this gate is about the 0o755 typed by hand.
var mkdirModeRe = regexp.MustCompile(`os\.MkdirAll\([^,]+,\s*(0o?[0-7]{3,4})\s*\)`)

// exemption is one file's licence to hand-type a mode, and how many times.
//
// The count is the point. This map used to hold reasons alone, so an exemption
// covered the whole FILE — and packages/core/session_portable.go was exempt for
// its EXPORT, which legitimately writes where the user asked, while the same
// file's ImportSession and BranchSession quietly created live transcripts in
// the data home at 0644 under a 0755 dir. One argued write sheltered two
// unargued ones. A bounded exemption cannot do that: adding a write to an
// exempt file fails the gate until someone raises the count on purpose.
type exemption struct {
	reason  string
	allowed int
}

// exemptFromOwnerOnly lists the files that write OUTSIDE $TERVA_HOME, each with
// the reason it may create a group- or world-accessible directory and the
// number of such writes it is allowed. Everything not listed is held to the
// owner-only rule.
//
// Adding an entry is a deliberate claim that the path is not under the data
// home. Check where the directory actually resolves before adding one — the
// whole point of the inversion below is that this list is short, argued, and
// reviewed, rather than long and assumed.
var exemptFromOwnerOnly = map[string]exemption{
	// Project files. `.terva/` is committed, shared, and read by everyone who
	// checks the repository out, so owner-only would be actively wrong. Nothing
	// secret may live there — ProjectConfig's type is the guard for that.
	filepath.Join("packages", "agent", "config", "extmcp.go"): {"writes the PROJECT .terva/config.json, a committed repo file", 1},
	filepath.Join("packages", "agent", "projectcmd.go"):       {"scaffolds .terva/ and writes the project config, both repo files", 2},

	// Paths the USER named. These tools exist to write where they are told, and
	// a 0700 directory appearing in someone's source tree would be a surprise.
	filepath.Join("packages", "agent", "tools", "write.go"):          {"the Write tool creates parents for a user-supplied path", 1},
	filepath.Join("packages", "agent", "tools", "generate_image.go"): {"writes the image to a user-supplied output path", 1},
	filepath.Join("packages", "core", "session_portable.go"):         {"portable export writes to a user-chosen destination", 1},

	// A private temp dir the OS already isolates, holding a tree that becomes
	// an executable and must stay traversable.
	filepath.Join("packages", "agent", "updatecmd.go"): {"extracts the update into a temp dir, not the data home", 1},

	// Build and developer tooling: repo files, never a runtime path.
	filepath.Join("cmd", "terva-i18n-lint", "main.go"):                  {"rewrites locale files in the repo", 1},
	filepath.Join("cmd", "terva-ste-lint", "baseline.go"):               {"writes the .ste baseline in the repo", 1},
	filepath.Join("examples", "extensions", "chat-loopback", "main.go"): {"example extension writing its own inbox", 1},
}

// scanForHandTypedModes walks root and returns "path:line: source" for every
// non-test Go file that hands os.MkdirAll a literal mode granting group or
// other any bit, skipping the files in exempt.
func scanForHandTypedModes(t *testing.T, root string, exempt map[string]exemption) []string {
	t.Helper()
	var offenders []string
	// How many hand-typed modes each exempt file actually has, so an exemption
	// argued for one write cannot silently cover a second. visited records which
	// exempt files this walk reached at all — the synthetic tree the
	// catches-a-new-offender test scans contains none of them, and an absent
	// file is the staleness test's business, not this count's.
	seen, visited := map[string]int{}, map[string]bool{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if testsupport.SkipScanDir(root, path, d) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		_, exempted := exempt[rel]
		if exempted {
			visited[rel] = true
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for i, line := range strings.Split(string(data), "\n") {
			code := line
			if idx := strings.Index(code, "//"); idx >= 0 {
				code = code[:idx]
			}
			m := mkdirModeRe.FindStringSubmatch(code)
			if m == nil {
				continue
			}
			mode, err := strconv.ParseUint(strings.TrimPrefix(strings.TrimPrefix(m[1], "0o"), "0"), 8, 32)
			if err != nil {
				continue
			}
			if mode&0o077 == 0 {
				continue
			}
			if exempted {
				seen[rel]++
				continue
			}
			offenders = append(offenders, rel+":"+strconv.Itoa(i+1)+": "+strings.TrimSpace(line))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// An exemption is a bounded claim, in both directions. More writes than
	// argued for means one arrived unreviewed under cover of the other; fewer
	// means the exemption outlived its reason and should shrink.
	for rel, ex := range exempt {
		if !visited[rel] {
			continue
		}
		switch got := seen[rel]; {
		case got > ex.allowed:
			offenders = append(offenders, rel+": "+strconv.Itoa(got)+" hand-typed modes but the exemption argues for "+
				strconv.Itoa(ex.allowed)+" ("+ex.reason+") — a write arrived under cover of one that was reviewed")
		case got < ex.allowed:
			offenders = append(offenders, rel+": exemption allows "+strconv.Itoa(ex.allowed)+
				" hand-typed mode(s) but the file now has "+strconv.Itoa(got)+"; shrink or remove it ("+ex.reason+")")
		}
	}
	sort.Strings(offenders)
	return offenders
}

// TestPrivateTreesAreOwnerOnly enforces that everything writing under
// $TERVA_HOME creates its directories owner-only.
//
// The bug this exists to stop was real and quiet: the host created
// ext-data/<name> at 0700 through privfs, and then the extension SDK's own
// helpers created SUBdirectories inside it at 0755 — as did two connector
// binaries around the state dir that holds a bot token. Nothing failed, because
// the 0700 parent was doing the work; the modes were wrong in a way that only
// mattered once someone moved, copied, or repaired the tree. And
// privfs.RepairOnce cannot save it: that walk is gated on a marker file and
// runs once per install, long before these directories exist.
//
// This gate scans the WHOLE repository and is default-deny. It used to scan a
// curated list of nine trees, and that list WAS the bug: a gate that enrolls
// its own subjects cannot fail when an offender is ADDED, which is the only
// moment it needed to. packages/agent/build alone had accumulated twelve
// hand-typed 0o755 directories under $TERVA_HOME — cards, worlds, groups,
// backgrounds, card history, user personas, the usage ledger, the audit log —
// and this test was green the entire time, because build/ was not on the list.
// persona/, i18n/, provider/, envcompat/ and worktree/ were in the same
// position, for the same reason.
//
// Whether a given path resolves under $TERVA_HOME is not decidable from the
// source: the directory usually arrives as a parameter, or through a helper
// several calls away. So the rule is inverted rather than made cleverer. EVERY
// hand-typed mode granting group or other any bit is an offence, and a file
// that legitimately writes outside the home argues its way into
// exemptFromOwnerOnly. New code is caught by default instead of missed by
// default.
//
// os.WriteFile is left alone on purpose: an extension staging a helper binary
// legitimately wants 0755 on the FILE, and privfs.WriteFile exists for when it
// does not.
func TestPrivateTreesAreOwnerOnly(t *testing.T) {
	offenders := scanForHandTypedModes(t, filepath.Join("..", ".."), exemptFromOwnerOnly)
	if len(offenders) > 0 {
		t.Errorf("group/other-accessible directory — use privfs.MkdirAll, or add the file to exemptFromOwnerOnly with a reason if it writes outside $TERVA_HOME:\n  %s",
			strings.Join(offenders, "\n  "))
	}
}

// TestOwnerOnlyGateCatchesANewOffender is the teeth on the gate above.
//
// A scan that reports nothing proves nothing on its own — it reads the same as
// a scan that walked no files, matched no pattern, or skipped every directory,
// which is exactly how the previous version stayed green over twelve real
// offenders. This plants one offender in a scratch tree and asserts the scan
// FINDS it, so a future edit that neuters the walk fails here instead of going
// quiet.
func TestOwnerOnlyGateCatchesANewOffender(t *testing.T) {
	root := testsupport.TempDir(t)
	newPkg := filepath.Join(root, "packages", "brandnew")
	if err := os.MkdirAll(newPkg, 0o755); err != nil {
		t.Fatal(err)
	}
	src := "package brandnew\n\nimport \"os\"\n\nfunc Setup(dir string) error {\n\treturn os.MkdirAll(dir, 0o755)\n}\n"
	if err := os.WriteFile(filepath.Join(newPkg, "store.go"), []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}

	offenders := scanForHandTypedModes(t, root, exemptFromOwnerOnly)

	want := filepath.Join("packages", "brandnew", "store.go") + ":6"
	found := false
	for _, o := range offenders {
		if strings.HasPrefix(o, want) {
			found = true
		}
	}
	if !found {
		t.Fatalf("gate did not catch a newly added 0o755 MkdirAll; a package added tomorrow would be missed the same way.\ngot offenders: %v", offenders)
	}

	// And it must stay quiet once the offender is fixed, or it is not a gate,
	// it is noise that everyone learns to ignore.
	fixed := "package brandnew\n\nimport \"terva.sh/terva/packages/privfs\"\n\nfunc Setup(dir string) error {\n\treturn privfs.MkdirAll(dir)\n}\n"
	if err := os.WriteFile(filepath.Join(newPkg, "store.go"), []byte(fixed), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := scanForHandTypedModes(t, root, exemptFromOwnerOnly); len(got) != 0 {
		t.Fatalf("gate still reports an offender after the fix: %v", got)
	}
}

// TestOwnerOnlyExemptionsAllExist keeps the exemption list honest. A stale
// entry — a file renamed or deleted — silently widens the gate's blind spot,
// and nothing else would ever notice.
func TestOwnerOnlyExemptionsAllExist(t *testing.T) {
	root := filepath.Join("..", "..")
	for rel, ex := range exemptFromOwnerOnly {
		if ex.reason == "" {
			t.Errorf("%s: exemption carries no reason", rel)
		}
		if ex.allowed < 1 {
			t.Errorf("%s: exemption allows %d writes, which is not an exemption", rel, ex.allowed)
		}
		if _, err := os.Stat(filepath.Join(root, rel)); err != nil {
			t.Errorf("%s: exempted file does not exist (stale entry widens the gate)", rel)
		}
	}
}
