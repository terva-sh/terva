package main

// The closure rule: a stable symbol promises every type it names. A stable
// func that takes an unstable type cannot keep its promise when that type
// changes, so the snapshot check refuses the pair. The rule reads syntax like
// the rest of this tool. It resolves a qualifier through the file's imports,
// and it follows only names inside this module.

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// rootPkg is how the manifest would list the module's root package.
const rootPkg = "."

// modulePath reads the module path from the repository's go.mod. Without it
// no import resolves to a package here, and the closure check would pass on
// nothing, so a missing go.mod is an error. The module line is read by hand:
// golang.org/x/mod is not a dependency, and one line does not earn one.
func modulePath(repo string) (string, error) {
	file := filepath.Join(repo, "go.mod")
	data, err := os.ReadFile(file)
	if err != nil {
		return "", fmt.Errorf("the closure check reads the module path: %w", err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		// A directive may carry a comment after it, and a path left with the
		// comment on would match no import: the check would pass on nothing.
		line, _, _ = strings.Cut(line, "//")
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "module"); ok && rest != "" && (rest[0] == ' ' || rest[0] == '\t') {
			if path := strings.Trim(strings.TrimSpace(rest), `"`); path != "" {
				return path, nil
			}
		}
	}
	return "", fmt.Errorf("%s declares no module", file)
}

// closure lists every stable symbol whose signature names an unstable one,
// and counts the references it followed. A name is unstable when its symbol
// carries the marker, or when its package is listed unstable or not listed
// at all: a package outside the manifest promises nothing.
//
// A name that resolves to nothing is reported too. The census reads syntax,
// so an import whose package clause differs from its path, or a type
// parameter this walk missed, would otherwise let a reference pass unseen.
//
// A name reached through an unexported type counts as named: a host uses
// the exported members of a value it cannot name (see throughHidden).
func closure(module string, entries []entry, censuses map[string]map[string]symbol) (leaks []string, followed int) {
	class := map[string]string{}
	for _, e := range entries {
		class[e.Pkg] = e.Class
	}
	for _, e := range entries {
		if e.Class != classStable {
			continue
		}
		syms := censuses[e.Pkg]
		for _, s := range sortedSymbols(syms) {
			if s.Unstable {
				continue
			}
			for _, r := range s.Refs {
				if leak, ok := leakOf(module, e.Pkg, s, r, class, censuses); ok {
					followed++
					if leak != "" {
						leaks = append(leaks, leak)
					}
				}
			}
		}
	}
	return leaks, followed
}

// leakOf judges one reference. ok is false for a reference outside the
// module, which the rule does not follow. leak is empty when the reference
// keeps the promise.
func leakOf(module, pkg string, s symbol, r ref, class map[string]string, censuses map[string]map[string]symbol) (leak string, ok bool) {
	where := fmt.Sprintf("%s: %s (%s)", pkg, s.Name, s.Kind)
	through := ""
	if r.Via != "" {
		through = " through " + r.Via
	}
	if r.Qual != "" && r.Path == "" {
		return fmt.Sprintf("%s names %s.%s%s, and no import of its file is called %s; alias the import to its package name",
			where, r.Qual, r.Name, through, r.Qual), true
	}
	target, name := pkg, r.Name
	if r.Path != "" {
		var rel string
		switch {
		case r.Path == module:
			rel = rootPkg
		case strings.HasPrefix(r.Path, module+"/"):
			rel = strings.TrimPrefix(r.Path, module+"/")
		default:
			return "", false
		}
		target, name = rel, r.Qual+"."+r.Name
	}
	name += through
	shown := target
	if target == rootPkg {
		shown = "the module's root package"
	}
	switch class[target] {
	case classStable:
	case classUnstable:
		return fmt.Sprintf("%s names %s, and %s is listed unstable", where, name, shown), true
	default:
		return fmt.Sprintf("%s names %s, and %s is not in %s", where, name, shown, manifestName), true
	}
	sym, found := censuses[target][r.Name]
	switch {
	case !found:
		return fmt.Sprintf("%s names %s, which %s does not export", where, name, shown), true
	case sym.Unstable:
		return fmt.Sprintf("%s names %s, which is marked Unstable:", where, name), true
	}
	return "", true
}

// printMarkerChanges lists the symbols whose marker differs between the
// recorded snapshot and the code, and returns how many. diffSymbols compares
// kinds and signatures, and a marker is neither.
func printMarkerChanges(w io.Writer, recorded, now map[string]symbol) int {
	var lines []string
	for name, s := range now {
		prev, ok := recorded[name]
		switch {
		case !ok || prev.Unstable == s.Unstable:
		case s.Unstable:
			lines = append(lines, fmt.Sprintf("    ! %s: marked Unstable:", name))
		default:
			lines = append(lines, fmt.Sprintf("    ! %s: no longer marked Unstable:", name))
		}
	}
	sort.Strings(lines)
	for _, l := range lines {
		fmt.Fprintln(w, l)
	}
	return len(lines)
}

// markedCount counts the symbols a census marks unstable.
func markedCount(syms map[string]symbol) int {
	n := 0
	for _, s := range syms {
		if s.Unstable {
			n++
		}
	}
	return n
}
