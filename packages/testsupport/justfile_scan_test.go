package testsupport

import (
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// The shell half of the nested-checkout rule that SkipScanDir enforces in Go.
//
// `gofmt -l .` and `gofmt -w .` walk the filesystem, and this project keeps git
// worktrees under .claude/worktrees/. So the lint gate went red naming a file in
// a branch the developer running it had never touched, and `just fmt` rewrote
// another session's uncommitted work in place. The Go guards were fixed for
// exactly this (see repowalk.go's comment) and the recipes were not, because
// nothing connected them.
//
// The fix is scripts/go-sources.sh — git's own answer to "what belongs to this
// checkout", which is stricter than the .gitignore line that happens to cover
// .claude/worktrees/: git does not descend into a nested checkout at all,
// ignored or not. This is what stops the walk-the-filesystem form coming back.
//
// Scoped to the justfile deliberately. scripts/release.sh runs `gofmt -l .` too,
// inside the release curation worktree — a freshly materialized public tree with
// nothing nested in it, where walking everything is the point.
func TestJustfileGofmtNeverWalksTheFilesystem(t *testing.T) {
	body, err := os.ReadFile("../../justfile")
	if err != nil {
		t.Fatal(err)
	}
	// `gofmt [-flags] .` — the walk-everything form. An explicit path list
	// (scripts/go-sources.sh piped in, or a named file) is what we want instead.
	walksCwd := regexp.MustCompile(`gofmt\s+(-\S+\s+)*\.(\s|$)`)

	found, offenders := 0, []string(nil)
	for i, line := range strings.Split(string(body), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			continue // a comment may describe the banned form (this fix does)
		}
		if !strings.Contains(trimmed, "gofmt") {
			continue
		}
		found++
		if walksCwd.MatchString(trimmed) {
			offenders = append(offenders, "justfile:"+strconv.Itoa(i+1)+": "+trimmed)
		}
	}
	// A scan that quietly matched nothing would pass no matter what the recipes
	// did — the same vacuity this whole family of guards keeps rediscovering.
	if found == 0 {
		t.Fatal("found no gofmt invocation in the justfile; the scan is not reading it")
	}
	if len(offenders) > 0 {
		t.Errorf("a recipe runs gofmt over the whole filesystem, which reaches the sibling worktrees under .claude/worktrees/ —\n"+
			"use `scripts/go-sources.sh` (this checkout's own sources, per git) instead:\n  %s",
			strings.Join(offenders, "\n  "))
	}
}
