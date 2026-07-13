// Package fswalk lists workspace files for @-mention pickers: one walk with
// one set of semantics (gitignore handling, .git pruning, entry/depth caps,
// dirs-first ordering) shared by the TUI's local FileSuggester and the
// daemon's files.list verb — so completion offers the same entries however
// the client connects.
package fswalk

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"terva.sh/terva/packages/ignore"
)

// Limits bound the recursive walk so pickers stay responsive in very large
// repos. Hitting either cap stops the walk early; the entries gathered so
// far are still usable, and List reports the truncation.
//
// The entry cap is set against the real bottleneck, which is not memory but
// the per-keystroke fuzzy ranking that scores every entry (~13 ms at 50k on
// a typical laptop — under one 60 Hz frame). The depth cap guards against
// pathologically deep trees.
const (
	MaxRecursiveEntries = 50000
	MaxRecursiveDepth   = 24
)

// alwaysSkipDir is never descended into, regardless of .gitignore. .git is
// repo-internal and never a useful @-mention target, and would otherwise
// blow the entry budget even with gitignore filtering off.
const alwaysSkipDir = ".git"

// Entry is one listed file or directory. Rel is the path relative to the
// walk root, in the OS separator.
type Entry struct {
	Rel   string
	IsDir bool
}

// Options selects what List returns.
type Options struct {
	// Dir is the subdirectory (relative to root) to list in flat mode; ""
	// lists the root itself. It must stay inside the root — List rejects
	// absolute paths and ".." escapes, because the daemon serves this from
	// client-supplied input. Ignored when Recursive is set (a recursive
	// walk always starts at the root, like the TUI's whole-tree mode).
	Dir string
	// Recursive walks the whole tree below root instead of one directory,
	// bounded by MaxRecursiveEntries / MaxRecursiveDepth.
	Recursive bool
	// RespectGitignore drops entries matched by the project's .gitignore
	// (the root one in flat mode; nested ones too in recursive mode).
	// .git is always dropped either way.
	RespectGitignore bool
}

// List returns the entries under root selected by opts, and whether the
// recursive walk stopped at a cap. Flat mode sorts directories first, then
// case-insensitively by name; recursive mode returns lexical walk order —
// both exactly what the TUI picker has always shown.
func List(root string, opts Options) ([]Entry, bool, error) {
	if opts.Recursive {
		entries, truncated := listRecursive(root, opts.RespectGitignore)
		return entries, truncated, nil
	}
	rel, err := cleanDir(opts.Dir)
	if err != nil {
		return nil, false, err
	}
	entries, err := listFlat(root, rel, opts.RespectGitignore)
	return entries, false, err
}

// cleanDir normalizes a client-supplied relative directory and rejects
// anything that could escape the root.
func cleanDir(dir string) (string, error) {
	if dir == "" {
		return "", nil
	}
	if filepath.IsAbs(dir) {
		return "", fmt.Errorf("fswalk: dir %q must be relative to the root", dir)
	}
	rel := filepath.Clean(filepath.FromSlash(dir))
	if rel == "." {
		return "", nil
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("fswalk: dir %q escapes the root", dir)
	}
	return rel, nil
}

func listFlat(root, browseRel string, respectGitignore bool) ([]Entry, error) {
	dir := root
	if browseRel != "" {
		dir = filepath.Join(root, browseRel)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var ig *ignore.Gitignore
	if respectGitignore {
		ig = ignore.Load(root)
	}
	var all []Entry
	for _, e := range entries {
		name := e.Name()
		rel := name
		if browseRel != "" {
			rel = filepath.Join(browseRel, name)
		}
		if respectGitignore {
			if e.IsDir() && name == alwaysSkipDir {
				continue
			}
			if ig.Match(filepath.ToSlash(rel), e.IsDir()) {
				continue
			}
		}
		all = append(all, Entry{Rel: rel, IsDir: e.IsDir()})
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].IsDir != all[j].IsDir {
			return all[i].IsDir
		}
		return strings.ToLower(filepath.Base(all[i].Rel)) < strings.ToLower(filepath.Base(all[j].Rel))
	})
	return all, nil
}

func listRecursive(root string, respectGitignore bool) ([]Entry, bool) {
	var stack *ignore.Stack
	if respectGitignore {
		stack = ignore.NewStack(root)
	}
	var all []Entry
	truncated := false
	rootSep := strings.Count(root, string(os.PathSeparator))
	// pushed mirrors the directories whose nested .gitignore we've brought
	// into scope on stack but not yet dropped. WalkDir is depth-first in
	// lexical order, so before each entry we pop frames for directories
	// we've finished walking (those no longer an ancestor of the current
	// path), then push the current directory. Honoring nested .gitignore
	// files is what stops a vendored node_modules whose ignore rule lives
	// in a subdirectory from swamping the entry budget before the walk
	// ever reaches the user's deeply nested source file.
	var pushed []string
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			if d != nil && d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if path == root {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return nil
		}
		relSlash := filepath.ToSlash(rel)
		if respectGitignore {
			// Drop stack frames for directories we've finished walking:
			// any pushed dir that is not an ancestor of this entry's dir.
			dirSlash := relSlash
			if !d.IsDir() {
				if i := strings.LastIndex(relSlash, "/"); i >= 0 {
					dirSlash = relSlash[:i]
				} else {
					dirSlash = ""
				}
			}
			for len(pushed) > 0 {
				top := pushed[len(pushed)-1]
				if top == dirSlash || strings.HasPrefix(dirSlash, top+"/") {
					break
				}
				pushed = pushed[:len(pushed)-1]
				stack.Pop()
			}
			// .gitignore patterns are matched against slash-separated paths.
			if stack.Match(relSlash, d.IsDir()) {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
		}
		if d.IsDir() {
			if d.Name() == alwaysSkipDir {
				return filepath.SkipDir
			}
			if strings.Count(path, string(os.PathSeparator))-rootSep >= MaxRecursiveDepth {
				return filepath.SkipDir
			}
			// This directory is kept; bring its nested .gitignore (if any)
			// into scope for the descendants we're about to visit.
			if respectGitignore {
				stack.Push(path, rel)
				pushed = append(pushed, relSlash)
			}
		}
		all = append(all, Entry{Rel: rel, IsDir: d.IsDir()})
		if len(all) >= MaxRecursiveEntries {
			truncated = true
			return filepath.SkipAll
		}
		return nil
	})
	return all, truncated
}
