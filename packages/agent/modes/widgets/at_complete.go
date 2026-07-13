package widgets

import (
	"sort"
	"strings"
)

// Shell-style Tab completion for an @-file token, over the same listing the
// picker popup shows (files.list over the wire when attached, local disk
// in-process). A pure function so the web composer's TypeScript port stays
// behavior-identical — both are pinned to the shared golden fixtures in
// testdata/at_complete_golden.json (the summarizeToolNames pattern).
//
// The semantics mirror TryPathTabCompleteEditor's bash posture, transposed
// from ReadDir to a workspace listing:
//
//   - completion is segment-wise: the token splits at its last "/" into a
//     parent and a base; candidates are the parent's direct children whose
//     name has the base as a (case-sensitive) prefix
//   - dot-names are hidden unless the base itself starts with "."
//   - one candidate completes fully — a directory gains a trailing "/" so
//     the token stays live and the next Tab descends; a file completes to
//     its full path (committing is Enter's job, in both clients)
//   - several candidates extend to their longest common prefix; when the
//     base already is that prefix, the token is left unchanged (the popup
//     is already showing the alternatives)
//   - Tab never commits and never fires a network request: it rewrites the
//     token text over the cached listing, nothing else

// AtCandidate is one listing entry for AtComplete: a "/"-separated path
// relative to the workspace root, and whether it is a directory.
type AtCandidate struct {
	Path string
	Dir  bool
}

// AtComplete returns the @-token query extended by one Tab press over
// entries, plus how many candidates matched. extended == query means the
// press changed nothing (no matches, or the base already sits at the
// deepest unambiguous prefix).
func AtComplete(entries []AtCandidate, query string) (extended string, n int) {
	parent := ""
	base := query
	if i := strings.LastIndex(query, "/"); i >= 0 {
		parent, base = query[:i+1], query[i+1:]
	}

	// The parent's direct children. A recursive listing carries every
	// directory as its own row, so children are exactly the rows whose
	// path is parent+name with no further "/"; a flat (one-directory)
	// listing feeds basename-shaped paths and parent is simply "".
	type child struct {
		name string
		dir  bool
	}
	seen := map[string]bool{}
	var kids []child
	for _, e := range entries {
		rest, ok := strings.CutPrefix(e.Path, parent)
		if !ok || rest == "" || strings.Contains(rest, "/") {
			continue
		}
		if !strings.HasPrefix(rest, base) {
			continue
		}
		if strings.HasPrefix(rest, ".") && !strings.HasPrefix(base, ".") {
			continue
		}
		if seen[rest] {
			continue
		}
		seen[rest] = true
		kids = append(kids, child{name: rest, dir: e.Dir})
	}
	if len(kids) == 0 {
		return query, 0
	}
	if len(kids) == 1 {
		ext := parent + kids[0].name
		if kids[0].dir {
			ext += "/"
		}
		return ext, 1
	}
	names := make([]string, len(kids))
	for i, k := range kids {
		names[i] = k.name
	}
	sort.Strings(names) // determinism; LCP is order-independent but tests aren't
	lcp := longestCommonPrefix(names)
	if lcp == base {
		return query, len(kids)
	}
	return parent + lcp, len(kids)
}
