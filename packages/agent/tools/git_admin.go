package tools

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// refuseGitAdmin refuses a write to git's own files: any path with a .git
// component, either as given or after symlinks resolve. That covers the .git
// directory of a repository and the .git file of a linked worktree.
//
// 🚨 Writing those files is as strong as running a command. A hook, a filter
// driver in config or info/attributes, core.fsmonitor, or a .git file that
// points at a gitdir the writer made, all run when git next runs there, and
// git runs there from the daemon too (the worktree engine) and from the
// person. write and edit need no approval in auto-edit, where bash does, so
// the write must take the bash path. Every session gets this, jailed or not.
// See notes on #1572 review rounds 4 to 6.
func refuseGitAdmin(path string) error {
	// 🚨 No lexical cleaning. The tool passes this exact string to the
	// kernel, which applies ".." after following each link. filepath.Abs
	// would drop link/.. before the walk and check a different file.
	abs := path
	if !filepath.IsAbs(abs) {
		wd, err := os.Getwd()
		if err != nil {
			return err
		}
		abs = wd + string(filepath.Separator) + abs
	}
	refused := fmt.Errorf("refused: %s is inside git's own files (.git). Writing there can make git run commands, so write and edit cannot. If the change is needed, run it with bash, which asks for approval", path)
	if hasGitComponent(abs) {
		return refused
	}
	// 🚨 Fail closed. A path whose links cannot be followed is refused,
	// and a dangling link is followed to its target, because writing through
	// it creates the target. A .git met anywhere on the way refuses too, so a
	// link named .git that points at a gitdir with another name is caught.
	resolved, err := followLinks(abs)
	if errors.Is(err, errPassesGit) || err == nil && hasGitComponent(resolved) {
		return refused
	}
	if err != nil {
		return fmt.Errorf("refused: the links in %s could not be followed (%v), so write and edit cannot tell whether it is inside git's own files", path, err)
	}
	return nil
}

// errPassesGit is followLinks' report of a .git component on the way.
var errPassesGit = errors.New("passes through .git")

// maxLinkHops bounds followLinks, so a symlink loop fails instead of spinning.
const maxLinkHops = 40

// followLinks resolves every symlink in abs, a dangling one included, without
// needing the target to exist. filepath.EvalSymlinks fails on a dangling link,
// and a caller that fell back to the literal path would miss where a write
// through the link lands.
//
// 🚨 It walks one component at a time and applies ".." to the path resolved
// so far, the way the kernel does. Cleaning a link target first would drop a
// ".." that, after a symlinked component, leads into .git. A volume name
// (a Windows drive or UNC share) is kept as the root.
func followLinks(abs string) (string, error) {
	vol := filepath.VolumeName(abs)
	done, rest := vol+string(filepath.Separator), splitPath(abs[len(vol):])
	for hops := 0; len(rest) > 0; {
		el := rest[0]
		rest = rest[1:]
		switch el {
		case ".":
			continue
		case "..":
			done = filepath.Dir(done)
			continue
		}
		if strings.EqualFold(el, ".git") {
			return "", errPassesGit
		}
		next := filepath.Join(done, el)
		fi, err := os.Lstat(next)
		if errors.Is(err, fs.ErrNotExist) {
			done = next
			continue
		}
		if err != nil {
			return "", err
		}
		if fi.Mode()&fs.ModeSymlink == 0 {
			done = next
			continue
		}
		if hops++; hops > maxLinkHops {
			return "", errors.New("too many symlinks")
		}
		target, err := os.Readlink(next)
		if err != nil {
			return "", err
		}
		// A relative target resolves against the link's directory, done.
		if filepath.IsAbs(target) {
			v := filepath.VolumeName(target)
			done, target = v+string(filepath.Separator), target[len(v):]
		}
		rest = append(splitPath(target), rest...)
	}
	return done, nil
}

// splitPath splits p into its non-empty elements, on either separator.
func splitPath(p string) []string {
	return strings.FieldsFunc(p, func(r rune) bool { return r == '/' || r == filepath.Separator })
}

// hasGitComponent reports whether any element of p is .git. The match ignores
// case, so a case-insensitive filesystem cannot reach .git as .GIT.
func hasGitComponent(p string) bool {
	for _, el := range strings.Split(filepath.ToSlash(p), "/") {
		if strings.EqualFold(el, ".git") {
			return true
		}
	}
	return false
}
