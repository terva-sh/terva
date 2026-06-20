package tools

import (
	"os"
	"path/filepath"
	"sort"
	"sync"
)

// maxWorkspaceFiles caps a workspace snapshot. Past it the differ disables
// itself for that root and reports nothing, rather than walk an unbounded
// tree at every turn boundary. Mirrors the file picker's entry budget
// (modes/file_suggest.go) so the two agree on what counts as "too big".
const maxWorkspaceFiles = 50000

// FileChange is one entry in a workspace diff: a workspace-relative,
// slash-separated path and what happened to it.
type FileChange struct {
	Path string
	Kind string // "added" | "modified" | "deleted"
}

// fileStamp is the cheap change signature for one file: modification time
// (unix nanos) and size. Reading it costs only the Lstat the walk already
// implies — no file contents are read, so the snapshot stays fast on large
// trees. Two stamps comparing unequal means the file was touched; the rare
// same-size-same-mtime edit is the accepted blind spot.
type fileStamp struct {
	mtime int64
	size  int64
}

// WorkspaceDiffer reports what changed in a workspace between two points in
// time by snapshotting (path -> stamp) and diffing. It honors .gitignore
// and prunes .git via the shared walkFiles, so build output and vendor
// trees never flood the diff. The root is read live through rootFn so the
// differ follows a /cd; Rebase captures the root it baselined at, and Diff
// measures against it. Safe for the serial agent-event goroutine; the mutex
// only guards against a concurrent reader of the last result.
type WorkspaceDiffer struct {
	rootFn func() string

	mu       sync.Mutex
	root     string               // root captured at the last Rebase
	baseline map[string]fileStamp // nil when disabled or unbaselined
	disabled bool                 // tripped when a root exceeds the cap
}

// NewWorkspaceDiffer returns a differ whose root is resolved on demand via
// rootFn (typically the sandbox's writable root, falling back to the cwd),
// so it tracks directory changes without re-wiring.
func NewWorkspaceDiffer(rootFn func() string) *WorkspaceDiffer {
	return &WorkspaceDiffer{rootFn: rootFn}
}

// Rebase snapshots the current workspace as the baseline that the next Diff
// measures against, reporting nothing itself. Call it at the start of each
// agent run (before any tool can touch the tree) so a diff captures exactly
// that run's changes. A root that exceeds the cap disables the differ until
// the root changes (a re-walk of a huge tree every turn is the thing the
// cap exists to prevent).
func (w *WorkspaceDiffer) Rebase() {
	if w == nil {
		return
	}
	root := w.rootFn()
	w.mu.Lock()
	stuck := w.disabled && root == w.root
	w.mu.Unlock()
	if stuck {
		return
	}
	snap, ok := snapshotTree(root)
	w.mu.Lock()
	w.root = root
	w.disabled = !ok
	if ok {
		w.baseline = snap
	} else {
		w.baseline = nil
	}
	w.mu.Unlock()
}

// Diff snapshots the workspace and returns what changed versus the baseline
// set by the last Rebase, sorted by path. It returns nil when nothing
// changed, the differ is disabled (oversized tree), or no baseline exists.
// The baseline is left intact — the next run's Rebase replaces it.
func (w *WorkspaceDiffer) Diff() []FileChange {
	if w == nil {
		return nil
	}
	w.mu.Lock()
	root, base, disabled := w.root, w.baseline, w.disabled
	w.mu.Unlock()
	if disabled || base == nil {
		return nil
	}
	cur, ok := snapshotTree(root)
	if !ok {
		return nil
	}

	var changes []FileChange
	for rel, cs := range cur {
		if bs, existed := base[rel]; !existed {
			changes = append(changes, FileChange{Path: rel, Kind: "added"})
		} else if cs != bs {
			changes = append(changes, FileChange{Path: rel, Kind: "modified"})
		}
	}
	for rel := range base {
		if _, still := cur[rel]; !still {
			changes = append(changes, FileChange{Path: rel, Kind: "deleted"})
		}
	}
	sort.Slice(changes, func(i, j int) bool {
		if changes[i].Path != changes[j].Path {
			return changes[i].Path < changes[j].Path
		}
		return changes[i].Kind < changes[j].Kind
	})
	return changes
}

// snapshotTree walks root (honoring .gitignore, pruning .git, skipping
// symlinks — the shared walkFiles semantics) and records a stamp per file.
// ok is false only when the tree exceeds maxWorkspaceFiles, in which case
// the partial map is discarded so a caller never diffs against a truncated
// baseline. An unreadable root yields an empty (usable) snapshot.
func snapshotTree(root string) (snap map[string]fileStamp, ok bool) {
	if root == "" {
		return map[string]fileStamp{}, true
	}
	snap = make(map[string]fileStamp)
	capped := false
	_ = walkFiles(root, true, func(abs, rel string) error {
		if len(snap) >= maxWorkspaceFiles {
			capped = true
			return filepath.SkipAll
		}
		info, err := os.Lstat(abs)
		if err != nil {
			return nil
		}
		snap[rel] = fileStamp{mtime: info.ModTime().UnixNano(), size: info.Size()}
		return nil
	})
	if capped {
		return nil, false
	}
	return snap, true
}
