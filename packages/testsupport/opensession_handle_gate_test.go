package testsupport

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// discardedOpenSession matches an assignment that calls core.OpenSession (or
// OpenSessionReconciled) and throws the *Session away: `_, msgs, err := ...`.
//
// The first return is the only one that carries the write handle, so `_` in
// THAT position is the whole defect. The match is anchored to the start of a
// statement for exactly that reason: `sess, _, err := core.OpenSession(...)`
// discards the messages and keeps the handle, which is correct and common, and
// an unanchored `_,` would flag every one of them.
var discardedOpenSession = regexp.MustCompile(
	`(^\s*|\bif\s+|;\s*)_\s*,\s*\w+\s*,\s*\w+\s*:?=\s*(\w+\.)?OpenSession(Reconciled)?\s*\(`)

// TestNoDiscardedOpenSessionHandle bans taking a session's WRITE handle for
// what is really a read.
//
// core.OpenSession returns a live O_APPEND|O_WRONLY handle and a bufio.Writer.
// Three call sites asked it only for the replayed messages and dropped the
// *Session on the floor: packages/agent/build/sessionread.go (every extension
// session read), packages/agent/tools/actor_spawn.go (reading a swarm child
// that is still running), and a third in the workspace that at least closed it.
// Each leaked a descriptor per call, and each held a write handle on a
// transcript another process may be appending to right now.
//
// That is also why this gate is worth its weight: once a session takes a
// cross-process lock, a discarded handle stops being a leak and becomes a
// refusal. The parent reading a running child's transcript would contend with
// the child forever, and nothing about the call site would say why.
//
// The remedy is core.ReadSessionMessages, or core.ReadSessionMeta when the
// caller also needs a field off the meta. Both replay read-only.
func TestNoDiscardedOpenSessionHandle(t *testing.T) {
	root := filepath.Join("..", "..")
	var offenders []string

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if SkipScanDir(root, path, d) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		// packages/core owns the function and reads it in its own tests, where
		// a discarded handle is the thing under test rather than a mistake.
		rel, relErr := filepath.Rel(root, path)
		if relErr == nil && strings.HasPrefix(filepath.ToSlash(rel), "packages/core/") {
			return nil
		}
		b, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil
		}
		for i, line := range strings.Split(string(b), "\n") {
			if discardedOpenSession.MatchString(line) {
				offenders = append(offenders,
					filepath.ToSlash(rel)+":"+strconv.Itoa(i+1)+": "+strings.TrimSpace(line))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}

	if len(offenders) > 0 {
		t.Errorf("these call sites take a session's write handle and discard it:\n  %s\n\n"+
			"OpenSession returns a live O_APPEND|O_WRONLY handle. Discarding it leaks a "+
			"descriptor, and once sessions take a cross-process lock it also holds that lock "+
			"until the process exits. Use core.ReadSessionMessages, or core.ReadSessionMeta "+
			"when you need the meta too.", strings.Join(offenders, "\n  "))
	}
}
