package swarm

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Archiving is one-way and an archived record is unreachable FROM TERVA. That
// is a standing rule, not a property of today's code, and it is the kind of
// rule that erodes one reasonable-looking commit at a time: first a count for
// the pane, then a list because the count raised a question, then "open it just
// to look" — and the archive is a second live tree with the growth problem it
// was built to remove.
//
// So the rule is enforced where it can actually fail: nothing outside this
// file's own implementation may name the archive directory or reach its path.
// A new read path has to delete this test to exist, which is a conversation
// rather than an oversight.
//
// It reads source rather than linking, for the reason every guard here does —
// what it is looking for is the ABSENCE of a call, and absence is not something
// a compiled test can observe.

// archiveHome is the only file allowed to know where the archive lives.
const archiveHome = "archive.go"

// archiveReach matches the ways a caller could locate the archive tree: the
// directory constant, the path helpers, or the literal name.
var archiveReach = regexp.MustCompile(`archiveDirName|archiveRoot\(|agentArchiveDir\(|"archive"`)

func TestNothingButArchiveGoCanReachTheArchive(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read swarm package: %v", err)
	}
	var offenders []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") {
			continue
		}
		// Tests may reach it — they are how the layout is checked at all, and
		// they are not a product read path.
		if name == archiveHome || strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(filepath.Clean(name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		for i, line := range strings.Split(string(src), "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "//") {
				continue // a comment naming the rule is not a breach of it
			}
			if archiveReach.MatchString(line) {
				offenders = append(offenders, name+":"+itoa(i+1)+": "+strings.TrimSpace(line))
			}
		}
	}
	// swarm.go's collision guard is the ONE sanctioned exception, and it goes
	// through archivedIDTaken rather than touching a path — so it does not
	// match, and the day it does is the day this needs re-deciding.
	if len(offenders) > 0 {
		t.Errorf("these reach the archive from outside archive.go:\n  %s\n\n"+
			"An archived record is unreachable from terva by design (see archive.go).\n"+
			"If a read path is genuinely wanted, that is a decision to make out loud —\n"+
			"delete this test deliberately rather than routing around it.",
			strings.Join(offenders, "\n  "))
	}
}

// A guard whose pattern matches nothing passes on a package that has fully
// regressed. Point it at the shapes it exists to catch.
func TestTheArchiveReachGuardRecognisesAReadPath(t *testing.T) {
	for _, bad := range []string{
		`	root := f.archiveRoot()`,
		`	entries, _ := os.ReadDir(f.agentArchiveDir(id))`,
		`	p := filepath.Join(f.cfg.Root, archiveDirName)`,
		`	dir := filepath.Join(root, "archive")`,
	} {
		if !archiveReach.MatchString(bad) {
			t.Errorf("guard missed a reach: %q", bad)
		}
	}
	for _, ok := range []string{
		`	if f.archivedIDTaken(id) {`,
		`	// the archive is unreachable from terva`,
		`	return fmt.Errorf("agent %s is already archived", a.ID)`,
	} {
		if archiveReach.MatchString(ok) {
			t.Errorf("guard fired on a non-reach: %q", ok)
		}
	}
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
