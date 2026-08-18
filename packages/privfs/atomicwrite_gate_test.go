package privfs_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"terva.sh/terva/packages/testsupport"
)

// writeExemption is one file's licence to call os.WriteFile despite knowing
// about privfs, and how many times.
//
// Bounded for the reason exemptFromOwnerOnly is bounded: a file-wide licence
// lets one argued write shelter an unargued one. Adding a write to an exempt
// file fails this gate until someone raises the count on purpose.
type writeExemption struct {
	reason  string
	allowed int
}

// exemptFromAtomicWrite lists the files that import privfs and still call
// os.WriteFile, with the reason each may. Everything else that imports privfs
// is held to privfs.WriteFile / privfs.WriteFileMode.
//
// The rule is scoped to privfs IMPORTERS rather than the whole tree on purpose.
// Whether a path resolves under $TERVA_HOME is not decidable from the source —
// it arrives as a parameter or through a helper several calls away — and a
// tree-wide ban would need thirty exemptions, which is a list nobody reads. A
// file that already imports privfs has demonstrably been told about the
// discipline; bypassing it one line after calling privfs.MkdirAll is the shape
// this gate exists to catch, and it is exactly what four security stores and
// both connector config savers were doing.
var exemptFromAtomicWrite = map[string]writeExemption{
	// 🪤 The card DIRECTORY's mtime is StoredCard.Added. An atomic write
	// creates and removes a temp entry in that directory, which bumps it, so
	// editing a card would restamp it as newly added and reorder the library.
	// Guarded by TestCardEditLeavesAddedAndTheCardDirAlone.
	filepath.Join("packages", "agent", "build", "cardstore.go"): {"an atomic write would bump the card dir mtime, which IS StoredCard.Added", 2},

	// Writes the TEMP half of their own temp+rename. Already atomic; routing
	// them through privfs would be a simplification, not a fix.
	filepath.Join("packages", "agent", "worktree", "registry.go"): {"writes the temp half of its own temp+rename", 1},
	filepath.Join("packages", "provider", "cache.go"):             {"writes the temp half of its own temp+rename", 1},
	filepath.Join("packages", "provider", "usermodels_write.go"):  {"writes the temp half of its own temp+rename", 1},

	// Files whose CONTENT is their existence. A torn read of a one-line marker
	// is indistinguishable from an intact one, so atomicity buys nothing.
	filepath.Join("packages", "agent", "corepack_offer.go"): {"a one-byte sentinel; its meaning is that it exists", 1},
	filepath.Join("packages", "envcompat", "envcompat.go"):  {"one-line migration markers; meaning is existence", 2},
	filepath.Join("packages", "agent", "chat", "daemon.go"): {"the pidfile, written once at startup before the daemon serves", 1},

	// Writes that never overwrite anything, so there is no window to tear.
	filepath.Join("cmd", "terva-telegram-connector", "main.go"): {"writes a new uniquely-named inbound file, never an existing one", 1},

	// Honours a mode the CALLER chose, for a tree that becomes an executable.
	filepath.Join("packages", "agent", "ext", "ext.go"): {"DataFS.WriteFile passes the extension's own perm through for staged binaries", 1},

	// Developer tooling rewriting repo files, not runtime state.
	filepath.Join("packages", "i18n", "capture.go"): {"rewrites locale files in the repo", 2},
}

// A file that imports privfs must not also call os.WriteFile.
//
// What a plain write costs: os.WriteFile opens O_TRUNC, so the live file is
// empty and then partially filled. $TERVA_HOME is explicitly shared between
// concurrent terva processes, and the readers do not degrade — LoadTrustStore
// treats a corrupt trusted.json as a hard error "so a security store is never
// silently treated as empty", which turns a torn read into a refusal to start.
// It also leaves an EXISTING file's mode alone, so a legacy 0644 store stays
// 0644 forever however many times it is rewritten.
//
// Scanned rather than listed, because a list of known offenders cannot fail
// when a new one is ADDED — which is the only moment it needed to.
func TestNobodyWhoKnowsAboutPrivfsBypassesIt(t *testing.T) {
	root := filepath.Join("..", "..")
	seen, visited := map[string]int{}, map[string]bool{}
	scanned, importers := 0, 0
	var offenders []string

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
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		scanned++
		src := string(data)
		// The package defines the helpers; it necessarily calls os.WriteFile.
		if strings.HasPrefix(rel, filepath.Join("packages", "privfs")) {
			return nil
		}
		if !strings.Contains(src, "terva.sh/terva/packages/privfs") {
			return nil
		}
		importers++
		if _, ok := exemptFromAtomicWrite[rel]; ok {
			visited[rel] = true
		}
		for i, line := range strings.Split(src, "\n") {
			code := line
			if idx := strings.Index(code, "//"); idx >= 0 {
				code = code[:idx]
			}
			if !strings.Contains(code, "os.WriteFile(") {
				continue
			}
			seen[rel]++
			if ex, ok := exemptFromAtomicWrite[rel]; ok && seen[rel] <= ex.allowed {
				continue
			}
			offenders = append(offenders, rel+":"+itoa(i+1)+": "+strings.TrimSpace(line))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if scanned < 500 {
		t.Fatalf("scanned only %d non-test Go files; the walk is broken and a pass here proves nothing", scanned)
	}
	if importers < 20 {
		t.Fatalf("only %d files import privfs; the import check is broken and this gate is inspecting nothing", importers)
	}
	sort.Strings(offenders)
	for _, o := range offenders {
		t.Errorf("%s\n  os.WriteFile truncates in place: a concurrent reader can see the empty window, and an "+
			"existing file keeps its old mode. Use privfs.WriteFile (0600) or privfs.WriteFileMode, or add the "+
			"file to exemptFromAtomicWrite with a reason and a count.", o)
	}

	// An exemption whose file no longer has that many writes is a widened
	// blind spot nothing else would notice.
	for rel, ex := range exemptFromAtomicWrite {
		if !visited[rel] {
			t.Errorf("exemptFromAtomicWrite names %s, which the scan never reached — it moved or stopped importing privfs", rel)
			continue
		}
		if seen[rel] != ex.allowed {
			t.Errorf("exemptFromAtomicWrite allows %d write(s) in %s (%s) but the file has %d; "+
				"lower the count so the spare licence cannot shelter a new one", ex.allowed, rel, ex.reason, seen[rel])
		}
	}
}

// A clean report and a walk that visited nothing read identically, which is
// precisely how the directory gate stayed green over twelve real offenders. So:
// plant one, assert the scan FINDS it; fix it, assert the scan goes quiet.
func TestTheAtomicWriteScanActuallyFindsAnOffender(t *testing.T) {
	dir := testsupport.TempDir(t)
	pkg := filepath.Join(dir, "packages", "faux")
	if err := os.MkdirAll(pkg, 0o755); err != nil {
		t.Fatal(err)
	}
	bad := "package faux\n\nimport (\n\t\"os\"\n\n\t\"terva.sh/terva/packages/privfs\"\n)\n\n" +
		"func Save(p string, b []byte) error {\n\t_ = privfs.MkdirAll(p)\n\treturn os.WriteFile(p, b, 0o600)\n}\n"
	if err := os.WriteFile(filepath.Join(pkg, "save.go"), []byte(bad), 0o600); err != nil {
		t.Fatal(err)
	}
	if n := countOffenders(t, dir); n != 1 {
		t.Fatalf("the scan found %d offenders in a tree containing exactly one; it is not looking", n)
	}

	good := strings.Replace(bad, "os.WriteFile(p, b, 0o600)", "privfs.WriteFile(p, b)", 1)
	good = strings.Replace(good, "\t\"os\"\n\n", "", 1)
	if err := os.WriteFile(filepath.Join(pkg, "save.go"), []byte(good), 0o600); err != nil {
		t.Fatal(err)
	}
	if n := countOffenders(t, dir); n != 0 {
		t.Fatalf("the scan still reports %d offenders after the fix; it is matching something else", n)
	}
}

// countOffenders runs the same match the gate does over an arbitrary tree.
func countOffenders(t *testing.T, root string) int {
	t.Helper()
	n := 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		src := string(data)
		if !strings.Contains(src, "terva.sh/terva/packages/privfs") {
			return nil
		}
		for _, line := range strings.Split(src, "\n") {
			code := line
			if idx := strings.Index(code, "//"); idx >= 0 {
				code = code[:idx]
			}
			if strings.Contains(code, "os.WriteFile(") {
				n++
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
