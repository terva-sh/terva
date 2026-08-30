package tools

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// notFound reports a missing path in the caller's own vocabulary while still
// unwrapping to the original error. The message is rebuilt rather than wrapped
// with %w because the point is to DROP what the original said: os.Stat renders
// the absolute path it was handed, and every tool here computes a display path
// precisely so absolute paths stay out of the context window. Unwrap keeps
// errors.Is(err, fs.ErrNotExist) working for any caller that asks.
type notFound struct {
	msg string
	err error
}

func (e *notFound) Error() string { return e.msg }
func (e *notFound) Unwrap() error { return e.err }

// notFoundError renders a missing-path failure for the model, and names the
// path the caller probably meant when the given path doubled the working
// directory.
//
// Only a not-exist error is rewritten. A permission error, a symlink loop, or a
// name-too-long keeps its original text, because there the operating system
// knows something the caller does not and the detail is the whole value.
//
// shown is the display path (see [Sandbox.DisplayPath]); given is the path the
// caller actually supplied, which is what the doubling check has to reason
// about. When given is empty there is no claim to make and the original error
// stands.
func notFoundError(cwd, given, shown string, err error) error {
	if err == nil {
		return nil
	}
	if !errors.Is(err, fs.ErrNotExist) || given == "" || shown == "" {
		return err
	}
	msg := shown + ": no such file or directory"
	if want := pathDoublingSuggestion(cwd, given); want != "" {
		msg += ". Did you mean " + want + "? A relative path resolves against the working directory, not the repository root."
	}
	return &notFound{msg: msg, err: err}
}

// pathDoublingSuggestion returns the path the caller probably meant when a
// repo-root-relative path was resolved against a working directory that
// already ends with that path's leading segments. It returns "" when there is
// nothing to suggest.
//
// The recorded case: cwd is .../projects/simple-vllm-monitoring-dashboard and
// the model asks for projects/simple-vllm-monitoring-dashboard/README.md,
// producing a resolved path with the project directory in it twice. The model
// used a path relative to the repository root while the working directory was
// already the subdirectory, which is an ordinary confusion and a trivially
// detectable one.
//
// The suggestion is PROVEN before it is made. A candidate is returned only if
// it actually exists on disk, so the tool never invents a second wrong path for
// a caller that was simply asking for a file that is not there. Matching is by
// whole segment, so a working directory ending in "-dashboard" cannot satisfy a
// path beginning "dash/".
func pathDoublingSuggestion(cwd, given string) string {
	if cwd == "" || given == "" || filepath.IsAbs(given) {
		return ""
	}
	gs := pathSegments(given)
	cs := pathSegments(cwd)
	// At least one segment has to overlap and at least one has to remain,
	// otherwise there is no shorter path to propose.
	if len(gs) < 2 || len(cs) == 0 {
		return ""
	}
	max := len(gs) - 1
	if len(cs) < max {
		max = len(cs)
	}
	// Longest overlap first: it yields the shortest remainder, which is the
	// reading that matches how the mistake is actually made.
	for k := max; k >= 1; k-- {
		if !segmentsEqual(gs[:k], cs[len(cs)-k:]) {
			continue
		}
		rem := filepath.Join(gs[k:]...)
		if _, err := os.Stat(filepath.Join(cwd, rem)); err == nil {
			return filepath.ToSlash(rem)
		}
	}
	return ""
}

// pathSegments splits a path into its meaningful segments, ignoring separator
// flavour, repeated separators, and "." components.
func pathSegments(p string) []string {
	var out []string
	for _, s := range strings.Split(filepath.ToSlash(p), "/") {
		if s == "" || s == "." {
			continue
		}
		out = append(out, s)
	}
	return out
}

func segmentsEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
