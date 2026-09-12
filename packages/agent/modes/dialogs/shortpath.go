package dialogs

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// shortenPath renders p the way the fish shell renders a prompt: the user's
// home directory becomes "~", and when the result is still wider than width
// every component except the last collapses to its first character. So
// /home/sothr/workspace/git-worktrees/terva-fixes reads ~/w/g/terva-fixes in
// 17 columns.
//
// The last component survives whole because it is the part that identifies the
// tree. The collapsed heads keep the path parseable by a reader who knows the
// machine: /w/g is unambiguous once you have one workspace directory, and a
// reader who needs the exact path opens the transcript view, which prints it in
// full.
//
// A path is displayed with forward slashes on every platform. This is display
// text, not a value anything opens, and a mixed-separator collapse reads worse
// than a normalised one.
func shortenPath(p string, width int) string {
	p = strings.TrimSpace(p)
	if p == "" || width <= 0 {
		return ""
	}
	p = tildeHome(filepath.ToSlash(p))
	if runeLen(p) <= width {
		return p
	}

	rooted := strings.HasPrefix(p, "/")
	parts := strings.Split(strings.Trim(p, "/"), "/")
	if len(parts) < 2 {
		return clipLeft(p, width)
	}
	heads := make([]string, 0, len(parts)-1)
	for _, seg := range parts[:len(parts)-1] {
		heads = append(heads, leadRunes(seg))
	}
	collapsed := strings.Join(append(heads, parts[len(parts)-1]), "/")
	if rooted {
		collapsed = "/" + collapsed
	}
	if runeLen(collapsed) <= width {
		return collapsed
	}
	// Every head is already one character, so the last component alone
	// overruns the cell. Keep its tail: two sibling worktrees differ at the
	// end of their names, never at the start.
	return clipLeft(collapsed, width)
}

// tildeHome rewrites a path under the user's home directory to start with "~".
// A path elsewhere is returned unchanged.
func tildeHome(p string) string {
	home := filepath.ToSlash(userHomeDir())
	if home == "" {
		return p
	}
	home = strings.TrimSuffix(home, "/")
	switch {
	case p == home:
		return "~"
	case strings.HasPrefix(p, home+"/"):
		return "~" + p[len(home):]
	}
	return p
}

// userHomeDir reads the home directory once. shortenPath runs for every row of
// every render, and os.UserHomeDir consults the environment on each call. It is
// a variable so a test can pin a home directory that does not depend on the
// machine running the test.
var userHomeDir = sync.OnceValue(func() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return home
})

// leadRunes collapses one path component to its first character, keeping the
// dot of a hidden directory. ".local" collapses to ".l", so a reader can still
// tell it from "logs".
func leadRunes(seg string) string {
	r := []rune(seg)
	switch {
	case len(r) == 0:
		return ""
	case r[0] == '.' && len(r) > 1:
		return string(r[:2])
	default:
		return string(r[:1])
	}
}

// clipLeft cuts s from the left to width runes and marks the cut, so the tail
// of the path survives. truncateLineSafe cuts from the right, which is correct
// for a description and wrong for a path.
func clipLeft(s string, width int) string {
	r := []rune(s)
	if len(r) <= width {
		return s
	}
	if width <= 1 {
		return string(r[len(r)-width:])
	}
	return "…" + string(r[len(r)-(width-1):])
}
