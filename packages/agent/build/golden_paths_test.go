package build

import (
	"path/filepath"
	"strings"
	"testing"
)

// slashAfterPlaceholders writes the separator that follows a path placeholder
// as "/", so a golden holds one form on every platform. The prompt joins
// paths with filepath.Join, so on Windows it names <TERVA_HOME>\docs where the
// golden says <TERVA_HOME>/docs. It rewrites only the separator directly after
// the placeholder, which is every path the goldens hold today.
func slashAfterPlaceholders(got string, sep rune) string {
	if sep == '/' {
		return got
	}
	for _, p := range []string{"<TERVA_HOME>", "<CWD>"} {
		got = strings.ReplaceAll(got, p+string(sep), p+"/")
	}
	return got
}

// normalizeGoldenPaths is slashAfterPlaceholders with this platform's
// separator.
func normalizeGoldenPaths(got string) string {
	return slashAfterPlaceholders(got, filepath.Separator)
}

// dropTaggedGroups removes the tool group lines that a build tag adds to the
// [inactive tool groups] note, so one golden serves the tagged and the untagged
// build. Each line must appear in every note first. A tagged build that stops
// listing its groups fails here, and the removal does not hide that.
func dropTaggedGroups(t *testing.T, got string) string {
	t.Helper()
	notes := strings.Count(got, "[inactive tool groups]")
	for _, line := range taggedGroupLines {
		if n := strings.Count(got, line); n != notes || n == 0 {
			t.Fatalf("%d [inactive tool groups] notes, but %q appears %d times", notes, line, n)
		}
		got = strings.ReplaceAll(got, line, "")
	}
	return got
}

// The Windows form, driven on any platform: a release once went to its public
// CI and failed there, because the goldens held "/" and Windows wrote "\".
func TestSlashAfterPlaceholders(t *testing.T) {
	in := `read <TERVA_HOME>\docs and <CWD>\a.txt, keep C:\x and <TERVA_HOME>`
	want := `read <TERVA_HOME>/docs and <CWD>/a.txt, keep C:\x and <TERVA_HOME>`
	if got := slashAfterPlaceholders(in, '\\'); got != want {
		t.Errorf("windows separator:\n got %q\nwant %q", got, want)
	}
	if got := slashAfterPlaceholders(in, '/'); got != in {
		t.Errorf("a slash separator must change nothing, got %q", got)
	}
}
