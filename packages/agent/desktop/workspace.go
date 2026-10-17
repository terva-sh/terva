package desktop

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// Source names how ChooseWorkspace found the workspace directory.
type Source string

const (
	// SourceExplicit is a directory --cwd named.
	SourceExplicit Source = "explicit"
	// SourceStart is the start directory, because it holds a team or is a
	// checkout.
	SourceStart Source = "start"
	// SourceRemembered is the directory the last launch used.
	SourceRemembered Source = "remembered"
	// SourcePicked is a directory the person chose in the picker.
	SourcePicked Source = "picked"
	// SourceFallback is the start directory, because nothing else answered.
	// The window does not remember it.
	SourceFallback Source = "fallback"
)

// ChooseWorkspace picks the directory the desktop Talkoot window serves.
//
// 🔑 A daemon serves the teams of the directory it runs in, and a desktop
// launcher usually starts the process in $HOME. The window would then show no
// teams, and a new team would bind to $HOME.
//
// The order is: an explicit --cwd, then the start directory when it holds a
// team or is a checkout, then the directory the last launch used when it
// still exists, then the person's choice in the picker. When the picker
// returns nothing, the start directory serves, and the source says so.
func ChooseWorkspace(path, start string, explicit bool, holdsTeam func(string) bool, pick func() string) (string, Source) {
	if explicit {
		return start, SourceExplicit
	}
	if holdsTeam(start) || isCheckout(start) {
		return start, SourceStart
	}
	if last := readState(path).Workspace; last != "" && isDir(last) {
		return last, SourceRemembered
	}
	if dir := pick(); dir != "" && isDir(dir) {
		if abs, err := filepath.Abs(dir); err == nil {
			dir = abs
		}
		return dir, SourcePicked
	}
	return start, SourceFallback
}

// RememberWorkspace records the directory a launch served, for the next
// launch.
func RememberWorkspace(path, dir string) error {
	if strings.TrimSpace(dir) == "" {
		return errors.New("desktop: no workspace to remember")
	}
	s := readState(path)
	s.Workspace = dir
	return writeState(path, s)
}

// SameDir reports whether a and b name one directory, after ~ expansion,
// making each absolute, and resolving symbolic links.
func SameDir(a, b string) bool {
	return normDir(a) == normDir(b)
}

func normDir(p string) string {
	if rest, ok := strings.CutPrefix(p, "~"); ok {
		if home, err := os.UserHomeDir(); err == nil {
			p = home + rest
		}
	}
	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}
	if real, err := filepath.EvalSymlinks(p); err == nil {
		p = real
	}
	return filepath.Clean(p)
}

func isDir(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

// isCheckout reports whether dir is the top of a git checkout. A worktree
// holds a .git file, so either kind counts.
func isCheckout(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, ".git"))
	return err == nil
}
