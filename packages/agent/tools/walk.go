package tools

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"terva.sh/terva/packages/ignore"
)

// walkFiles walks the tree under root in deterministic (lexical) order,
// invoking fn for each regular file with its absolute path and its
// slash-separated path relative to root. The shared traversal behind the
// grep and glob tools, so the two can't drift on what they skip.
//
// .git is always pruned. Symlinks (file or dir) are skipped entirely: a
// symlinked directory is never descended and a symlinked file is never
// read, which keeps the walk inside the tree even though the cwd jail is
// a guardrail rather than a hard sandbox. When respectIgnore is true the
// root .gitignore and every nested .gitignore prune the walk, mirroring
// git's nearest-file-wins semantics via ignore.Stack.
//
// fn may return filepath.SkipAll to stop the walk early (e.g. once a
// result page is full); any other error aborts and is returned.
func walkFiles(root string, respectIgnore bool, fn func(abs, rel string) error) error {
	var stack *ignore.Stack
	if respectIgnore {
		stack = ignore.NewStack(root)
	}
	// pushed mirrors the directories whose nested .gitignore is currently
	// in scope on stack. WalkDir is depth-first in lexical order, so before
	// each entry we pop frames for directories we've finished walking, then
	// push the directory we're about to descend. This is the same
	// bookkeeping the @-file picker uses (modes/file_suggest.go).
	var pushed []string
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if d != nil && d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if path == root {
			return nil
		}
		// Skip symlinks outright (containment): a symlinked dir is not
		// descended, a symlinked file is not handed to fn.
		if d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return nil
		}
		relSlash := filepath.ToSlash(rel)

		if respectIgnore {
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
			if stack.Match(relSlash, d.IsDir()) {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
		}

		if d.IsDir() {
			if d.Name() == ".git" {
				return filepath.SkipDir
			}
			if respectIgnore {
				stack.Push(path, rel)
				pushed = append(pushed, relSlash)
			}
			return nil
		}
		return fn(path, relSlash)
	})
}

// matchGlob reports whether name (a slash-separated relative path)
// matches pattern. It supports ** (any number of path segments,
// including zero), plus * ? and [...] within a single segment via
// filepath.Match. A pattern with no ** matches against the path
// structure literally, so "**/*.go" is recursive while "*.go" matches
// only top-level files.
func matchGlob(pattern, name string) bool {
	return matchSegments(strings.Split(pattern, "/"), strings.Split(name, "/"))
}

func matchSegments(pat, name []string) bool {
	for len(pat) > 0 {
		if pat[0] == "**" {
			// Trailing ** matches anything remaining (including nothing).
			if len(pat) == 1 {
				return true
			}
			// Try to match the rest of the pattern at every suffix.
			for i := 0; i <= len(name); i++ {
				if matchSegments(pat[1:], name[i:]) {
					return true
				}
			}
			return false
		}
		if len(name) == 0 {
			return false
		}
		if ok, err := filepath.Match(pat[0], name[0]); err != nil || !ok {
			return false
		}
		pat = pat[1:]
		name = name[1:]
	}
	return len(name) == 0
}

// resolveSearchRoot resolves an optional path argument (relative to cwd)
// to an absolute path and stats it, so grep/glob can branch on
// file-vs-directory. An empty arg defaults to cwd.
func resolveSearchRoot(cwd, arg string) (abs string, isDir bool, err error) {
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	target := cwd
	if arg != "" {
		target = resolvePath(cwd, arg)
	}
	info, statErr := os.Stat(target)
	if statErr != nil {
		return target, false, statErr
	}
	return target, info.IsDir(), nil
}
