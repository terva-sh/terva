package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A golden corpus that is byte-compared against in-source frames must be pinned
// to LF in .gitattributes, or it fails on Windows and nowhere else.
//
// This has now cost two releases running. connproto's corpus was pinned in
// v0.131.3's final commit; extproto's arrived one release later with the same
// shape, the same test name (TestGoldenFixtureMatchesCorpus), and no pin — so
// v0.131.4's go-live failed on `test (windows-latest)` with "no longer matches
// the in-source corpus", a message that names nothing about line endings.
//
// The internal forge has no Windows runner, so `just ci` cannot see this class
// at all: the FIRST signal is the public release gate, which is the most
// expensive place there is to learn it.
//
// Written self-enrolling — the corpora are SCANNED, never listed — so the next
// package to publish one is covered on the commit that adds it, without anyone
// remembering this file exists.
func TestEveryGoldenCorpusIsPinnedToLF(t *testing.T) {
	root := filepath.Join("..", "..")

	attrs, err := os.ReadFile(filepath.Join(root, ".gitattributes"))
	if err != nil {
		t.Fatalf("read .gitattributes: %v", err)
	}
	var pinned []string
	for _, line := range strings.Split(string(attrs), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || !strings.Contains(line, "eol=lf") {
			continue
		}
		pinned = append(pinned, strings.Fields(line)[0])
	}
	if len(pinned) == 0 {
		t.Fatal("parsed no eol=lf patterns from .gitattributes — the file moved or changed shape, " +
			"and an empty result would pass every check below vacuously")
	}

	// Every testdata corpus under packages/, whatever package owns it.
	var corpora []string
	err = filepath.WalkDir(filepath.Join(root, "packages"), func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		ext := filepath.Ext(path)
		if ext != ".jsonl" && ext != ".json" {
			return nil
		}
		if !strings.Contains(filepath.ToSlash(path), "/testdata/") {
			return nil
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			return rerr
		}
		corpora = append(corpora, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		t.Fatalf("walk packages/: %v", err)
	}
	if len(corpora) == 0 {
		t.Fatal("found no testdata corpora — the scan is broken, and every assertion below " +
			"would pass over an empty list")
	}

	// Only the corpora a test byte-compares can fail this way; a fixture merely
	// UNMARSHALLED does not care about its line endings. Rather than guess which
	// is which, the check applies to any corpus whose own package byte-compares
	// something — the same signal, read from the test source.
	for _, rel := range corpora {
		dir := filepath.Dir(rel)
		if !packageComparesBytes(t, filepath.Join(root, filepath.Dir(dir))) {
			continue
		}
		if coveredByPin(rel, pinned) {
			continue
		}
		t.Errorf("%s is byte-compared by its package but no .gitattributes eol=lf pattern covers it:\n  %s\n"+
			"A Windows checkout converts it to CRLF, the comparison fails there and ONLY there, and the "+
			"first signal is the public release gate. Add a pin beside the others.",
			rel, strings.Join(pinned, "\n  "))
	}
}

// packageComparesBytes reports whether any _test.go in dir compares raw bytes
// against a testdata file — the property that makes line endings load-bearing.
func packageComparesBytes(t *testing.T, dir string) bool {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		src, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		body := string(src)
		if strings.Contains(body, "testdata/") &&
			(strings.Contains(body, "bytes.Equal") || strings.Contains(body, "string(want)") ||
				strings.Contains(body, "no longer matches")) {
			return true
		}
	}
	return false
}

// coveredByPin matches a repo-relative path against the .gitattributes patterns
// this repo actually uses: a literal path, a "<dir>/*.<ext>" glob, or a
// "<dir>/**/*.<ext>" recursive one.
func coveredByPin(rel string, pinned []string) bool {
	for _, pat := range pinned {
		if pat == rel {
			return true
		}
		if i := strings.Index(pat, "/**/"); i >= 0 {
			prefix, suffix := pat[:i], pat[i+len("/**/"):]
			if strings.HasPrefix(rel, prefix+"/") && matchGlob(suffix, filepath.Base(rel)) {
				return true
			}
			continue
		}
		if filepath.Dir(pat) == filepath.Dir(rel) && matchGlob(filepath.Base(pat), filepath.Base(rel)) {
			return true
		}
	}
	return false
}

func matchGlob(pat, name string) bool {
	ok, err := filepath.Match(pat, name)
	return err == nil && ok
}
