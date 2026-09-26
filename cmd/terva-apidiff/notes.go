package main

// Notes mode: every stable break since the last release has a migration note.
//
// Decision 0026 promises a note for each stable break, in the release that
// ships it. The notes live in docs/migrating.md, and the ones not yet released
// sit under its "## Unreleased" heading. Each note ends with a Covers:
// paragraph that names what it covers in backquotes: a symbol
// (`packages/core.Agent.Run`), a type with its members (`packages/core.Agent`),
// a package (`packages/provider/auth`), or a package and every package below
// it (`packages/provider/...`). `-notes` compares those names with the breaks
// `-since` counts.
//
// A stable break needs a note above the "### Unstable changes (no promise)"
// heading. A note under it is optional, and it covers only unstable breaks, so
// a courtesy note cannot pass for a promised one. A name that covers no break
// of its class is a finding too: a typo, or a note a revert left behind, would
// otherwise read as coverage.
//
// Before a cut, `-seal vX.Y.Z` renames "## Unreleased" to "## vX.Y.Z" and opens
// a fresh Unreleased section above it. The check reads every version section
// newer than the ref as well as Unreleased, so sealed notes still count until
// their release publishes and its pub/ tag becomes the ref. `-release vX.Y.Z`
// is the cut's form: it also finds notes left unsealed, and a sealed section
// for any other version, since neither would reach this release's page.
//
// Without `-require` the report exits zero on a finding. Both CI lanes and
// `just migration-notes` pass it (TKT-01M3CVDW31).

import (
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

const (
	unreleasedHeading = "## Unreleased"
	unstableHeading   = "### Unstable changes (no promise)"
	coversLabel       = "Covers:"
	// placeholder stands in an empty section. The seal drops it.
	placeholder = "No notes yet."
)

var (
	versionHeading = regexp.MustCompile(`^## (v\d+\.\d+\.\d+)$`)
	trailingSemver = regexp.MustCompile(`v(\d+)\.(\d+)\.(\d+)$`)
)

// semver reads the vX.Y.Z at the end of s, as in v0.139.0 or pub/v0.138.2.
func semver(s string) ([3]int, bool) {
	m := trailingSemver.FindStringSubmatch(s)
	if m == nil {
		return [3]int{}, false
	}
	var v [3]int
	for i := range v {
		n, err := strconv.Atoi(m[i+1])
		if err != nil {
			return [3]int{}, false
		}
		v[i] = n
	}
	return v, true
}

func newerThan(a, b [3]int) bool {
	for i := range a {
		if a[i] != b[i] {
			return a[i] > b[i]
		}
	}
	return false
}

// cover is one name a Covers: paragraph lists.
type cover struct {
	Line int
	Text string
	Pkg  string
	// Name is a symbol, or a type that covers its members too. "" covers the
	// whole package.
	Name string
	// Tree covers Pkg and every package below it.
	Tree bool
	// Class is stable above the unstable heading and unstable under it.
	Class string
}

func (c cover) matches(b breakage) bool {
	switch {
	case b.Pkg == c.Pkg:
	case c.Tree && strings.HasPrefix(b.Pkg, c.Pkg+"/"):
	default:
		return false
	}
	return c.Name == "" || b.Name == c.Name || strings.HasPrefix(b.Name, c.Name+".")
}

// breakage is one symbol removed or changed since the ref, in the class it
// breaks in.
type breakage struct {
	Pkg, Name, Kind, Class string
	Removed                bool
}

func breaksSince(pkgs []sincePkg) []breakage {
	var out []breakage
	for _, p := range pkgs {
		for _, s := range p.Diff.Removed {
			out = append(out, breakage{p.Pkg, s.Name, s.Kind, breakClass(p.Then, s.Unstable), true})
		}
		for _, c := range p.Diff.Changed {
			out = append(out, breakage{p.Pkg, c.Symbol.Name, c.Symbol.Kind, breakClass(p.Then, c.WasUnstable), false})
		}
	}
	return out
}

var backquoted = regexp.MustCompile("`([^`]*)`")

// notesDoc is what parseNotes read.
type notesDoc struct {
	Covers []cover
	// Sealed lists the version sections it read: those newer than the ref,
	// whose release has not published.
	Sealed []string
	// Pending counts the notes under Unreleased.
	Pending int
}

const (
	lineText  = iota
	lineFence // a line that opens or closes a code fence
	lineCode  // a line inside a code fence
)

// splitLines splits a page into lines and marks each one's part in a code
// fence. An unclosed fence is an error.
func splitLines(data []byte) ([]string, []int, error) {
	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	kinds := make([]int, len(lines))
	fence := ""
	for i, l := range lines {
		t := strings.TrimSpace(l)
		switch {
		case fence != "" && strings.HasPrefix(t, fence):
			kinds[i], fence = lineFence, ""
		case fence != "":
			kinds[i] = lineCode
		case strings.HasPrefix(t, "```") || strings.HasPrefix(t, "~~~"):
			kinds[i], fence = lineFence, t[:3]
		}
	}
	if fence != "" {
		return nil, nil, fmt.Errorf("a code fence opened with %s never closes", fence)
	}
	return lines, kinds, nil
}

// parseNotes reads the Covers: paragraphs of the Unreleased section, and of
// each version section newer than ref. Fenced code is skipped, so the page can
// show the format. A missing or repeated section, an unstable subsection that
// is not last, and a name it cannot read are errors, never an empty result: a
// section the parser missed would report every break as uncovered, and one it
// half read could hide a finding.
//
// A Covers: paragraph counts only inside a note: under a ### heading, or a
// #### heading in the unstable subsection, after text that tells a host what
// to do. Without that rule a bare list of names would pass for coverage and
// give a host nothing to act on.
func parseNotes(data []byte, ref string) (notesDoc, error) {
	var doc notesDoc
	lines, kinds, err := splitLines(data)
	if err != nil {
		return doc, err
	}
	refV, refOK := semver(ref)
	const (
		skip = iota
		stable
		unstable
	)
	state, unreleased, seen := skip, 0, map[string]bool{}
	section := ""
	// note is the line of the current note's heading, 0 outside a note, and
	// guided records that the note has said something before its Covers:.
	note, guided := 0, false
	for i := 0; i < len(lines); i++ {
		line := strings.TrimRight(lines[i], " \t")
		trimmed := strings.TrimSpace(line)
		switch kinds[i] {
		case lineFence:
			continue
		case lineCode:
			// A code example in a note is guidance too, but an empty
			// fence is not.
			guided = guided || (state != skip && note > 0 && trimmed != "")
			continue
		}
		if strings.HasPrefix(line, "## ") || line == "##" {
			state, note, section = skip, 0, ""
			if line == unreleasedHeading {
				unreleased++
				state, section = stable, "Unreleased"
			} else if m := versionHeading.FindStringSubmatch(line); m != nil && refOK {
				if v, _ := semver(m[1]); newerThan(v, refV) {
					if seen[m[1]] {
						return doc, fmt.Errorf("line %d: a second %q", i+1, line)
					}
					seen[m[1]] = true
					doc.Sealed = append(doc.Sealed, m[1])
					state, section = stable, m[1]
				}
			}
			continue
		}
		if state == skip {
			continue
		}
		switch {
		case line == unstableHeading:
			if state == unstable {
				return doc, fmt.Errorf("line %d: a second %q in %q", i+1, unstableHeading, "## "+section)
			}
			state, note = unstable, 0
			continue
		case state == unstable && strings.HasPrefix(line, "### "):
			return doc, fmt.Errorf("line %d: %q comes after %q; the unstable subsection must come last, so each note's class is plain", i+1, line, unstableHeading)
		case state == stable && strings.HasPrefix(line, "### "), state == unstable && strings.HasPrefix(line, "#### "):
			note, guided = i+1, false
			if section == "Unreleased" {
				doc.Pending++
			}
			continue
		}
		if !strings.HasPrefix(trimmed, coversLabel) {
			guided = guided || (note > 0 && trimmed != "" && !strings.HasPrefix(trimmed, "#"))
			continue
		}
		heading := "###"
		if state == unstable {
			heading = "####"
		}
		switch {
		case note == 0:
			return doc, fmt.Errorf("line %d: %s outside a note; put it under a %s heading that names the change", i+1, coversLabel, heading)
		case !guided:
			return doc, fmt.Errorf("line %d: the note at line %d says nothing before its %s; tell a host what to change", i+1, note, coversLabel)
		}
		class := classStable
		if state == unstable {
			class = classUnstable
		}
		// The paragraph runs to a blank line, a heading or a fence. The
		// outer loop then reads that line, so a fence after the paragraph
		// is still skipped.
		start, names := i, 0
		for ; i < len(lines); i++ {
			text := strings.TrimSpace(lines[i])
			if text == "" || strings.HasPrefix(text, "#") || kinds[i] != lineText {
				i--
				break
			}
			for _, m := range backquoted.FindAllStringSubmatch(text, -1) {
				c, err := parseCover(m[1])
				if err != nil {
					return doc, fmt.Errorf("line %d: %w", i+1, err)
				}
				c.Line, c.Class = i+1, class
				doc.Covers = append(doc.Covers, c)
				names++
			}
		}
		if names == 0 {
			return doc, fmt.Errorf("line %d: %s names nothing; put each symbol, type or package in backquotes", start+1, coversLabel)
		}
	}
	switch {
	case unreleased == 0:
		return doc, fmt.Errorf("no %q heading; the notes for the next release go under it", unreleasedHeading)
	case unreleased > 1:
		return doc, fmt.Errorf("%d %q headings; keep one", unreleased, unreleasedHeading)
	}
	return doc, nil
}

// parseCover reads one backquoted name: a package directory, then optionally
// a dot and a symbol (packages/core.Agent.Run), or /... for the package and
// every package below it.
func parseCover(s string) (cover, error) {
	c := cover{Text: s}
	bad := func(why string) (cover, error) {
		return cover{}, fmt.Errorf("`%s`: %s; write a package directory such as packages/core, packages/core.Agent, or packages/provider/...", s, why)
	}
	if s == "" || strings.ContainsAny(s, " \t") {
		return bad("not a single name")
	}
	pkg := s
	if strings.HasSuffix(s, "/...") {
		pkg, c.Tree = strings.TrimSuffix(s, "/..."), true
	}
	slash := strings.LastIndex(pkg, "/")
	if dot := strings.Index(pkg[slash+1:], "."); dot >= 0 {
		if c.Tree {
			return bad("/... takes a package, not a symbol")
		}
		pkg, c.Name = pkg[:slash+1+dot], pkg[slash+1+dot+1:]
		if c.Name == "" || strings.HasPrefix(c.Name, ".") || strings.HasSuffix(c.Name, ".") || strings.Contains(c.Name, "..") {
			return bad("the symbol after the package is empty")
		}
	}
	if pkg == "" || strings.HasPrefix(pkg, "/") || strings.HasSuffix(pkg, "/") || strings.Contains(pkg, "//") {
		return bad("the package directory is empty or malformed")
	}
	c.Pkg = pkg
	return c, nil
}

// sealNotes moves the notes under Unreleased to a new "## version" section,
// and opens an empty Unreleased section above it. It reports how many notes
// it moved. With none it returns the page unchanged, because a release with
// no notes gets no section and its page links none.
//
// The sealed section drops the "No notes yet." placeholders and an unstable
// subsection that holds no note, so a release shows only what it carries.
// count, when set, opens the section: the per-class count decision 0026 puts
// in each release's notes.
func sealNotes(data []byte, version, count string) ([]byte, int, error) {
	if v := versionHeading.FindStringSubmatch("## " + version); v == nil {
		return nil, 0, fmt.Errorf("%q is not a version such as v0.139.0", version)
	}
	// The ref names no version, so this reads Unreleased alone and holds
	// the page to the rules the check holds it to.
	doc, err := parseNotes(data, "")
	if err != nil {
		return nil, 0, err
	}
	// Nothing to seal, whether the release has no notes or they are sealed
	// already, so a repeated seal changes nothing.
	if doc.Pending == 0 {
		return data, 0, nil
	}
	lines, kinds, err := splitLines(data)
	if err != nil {
		return nil, 0, err
	}
	start, end := -1, len(lines)
	for i, l := range lines {
		if kinds[i] != lineText {
			continue
		}
		l = strings.TrimRight(l, " \t")
		switch {
		case l == "## "+version:
			return nil, 0, fmt.Errorf("line %d: the page already has a %q section, and more notes arrived under %q since; move them into it by hand", i+1, l, unreleasedHeading)
		case l == unreleasedHeading:
			start = i
		case start >= 0 && end == len(lines) && i > start && (strings.HasPrefix(l, "## ") || l == "##"):
			end = i
		}
	}
	var body []string
	unstableAt, unstableNotes := -1, 0
	for i := start + 1; i < end; i++ {
		l := strings.TrimRight(lines[i], " \t")
		if kinds[i] == lineText {
			switch {
			case l == placeholder:
				continue
			case l == unstableHeading:
				unstableAt = len(body)
			case unstableAt >= 0 && strings.HasPrefix(l, "#### "):
				unstableNotes++
			}
		}
		body = append(body, lines[i])
	}
	if unstableAt >= 0 && unstableNotes == 0 {
		body = body[:unstableAt]
	}
	// One blank line between blocks, none at either end.
	var tidy []string
	for _, l := range body {
		if strings.TrimSpace(l) == "" && (len(tidy) == 0 || strings.TrimSpace(tidy[len(tidy)-1]) == "") {
			continue
		}
		tidy = append(tidy, l)
	}
	for len(tidy) > 0 && strings.TrimSpace(tidy[len(tidy)-1]) == "" {
		tidy = tidy[:len(tidy)-1]
	}
	out := append([]string{}, lines[:start]...)
	out = append(out, unreleasedHeading, "", placeholder, "", unstableHeading, "", placeholder, "", "## "+version, "")
	if count != "" {
		out = append(out, count, "")
	}
	out = append(out, tidy...)
	if end < len(lines) {
		out = append(out, "")
	}
	out = append(out, lines[end:]...)
	return []byte(strings.TrimRight(strings.Join(out, "\n"), "\n") + "\n"), doc.Pending, nil
}

// breakCount words the per-class count of breaks since ref for the top of a
// sealed section.
func breakCount(breaks []breakage, ref string) string {
	var removed, changed, unstable int
	for _, b := range breaks {
		switch {
		case b.Class != classStable:
			unstable++
		case b.Removed:
			removed++
		default:
			changed++
		}
	}
	since := ref
	if v, ok := semver(ref); ok {
		since = fmt.Sprintf("v%d.%d.%d", v[0], v[1], v[2])
	}
	if removed+changed == 0 {
		return fmt.Sprintf("Since %s: no stable break, and %d unstable break(s).", since, unstable)
	}
	return fmt.Sprintf("Since %s: %d stable break(s) (%d removed, %d changed), each with a note below, and %d unstable break(s).",
		since, removed+changed, removed, changed, unstable)
}

// notesReport compares the breaks since ref with the notes' Covers: names. It
// reports whether it found nothing: every stable break has a note, and every
// name covers a break of its class.
//
// release, when set, is the version being cut. The notes must then sit in its
// section: a note still under Unreleased, or a sealed section for another
// version, would not reach this release's page.
func notesReport(repo, dir, notes, ref, release string, w io.Writer) (bool, error) {
	if release != "" && versionHeading.FindStringSubmatch("## "+release) == nil {
		return false, fmt.Errorf("-release %q is not a version such as v0.139.0", release)
	}
	data, err := os.ReadFile(notes)
	if err != nil {
		return false, err
	}
	doc, err := parseNotes(data, ref)
	if err != nil {
		return false, fmt.Errorf("%s: %w", notes, err)
	}
	covers := doc.Covers
	pkgs, err := diffSince(repo, dir, ref)
	if err != nil {
		return false, err
	}
	breaks := breaksSince(pkgs)
	used := make([]bool, len(covers))
	var missing []breakage
	var stableN, unstableN, unstableNoted int
	for _, b := range breaks {
		noted := false
		for i, c := range covers {
			if c.Class == b.Class && c.matches(b) {
				used[i], noted = true, true
			}
		}
		switch b.Class {
		case classStable:
			stableN++
			if !noted {
				missing = append(missing, b)
			}
		default:
			unstableN++
			if noted {
				unstableNoted++
			}
		}
	}
	var stale []string
	for i, c := range covers {
		if used[i] {
			continue
		}
		line := fmt.Sprintf("%s:%d: `%s` covers no %s break since %s", notes, c.Line, c.Text, c.Class, ref)
		for _, b := range breaks {
			if b.Class != c.Class && c.matches(b) {
				if c.Class == classStable {
					line += fmt.Sprintf("; it names an unstable break, whose note goes under %q", unstableHeading)
				} else {
					line += "; it names a stable break, whose note goes above the unstable subsection"
				}
				break
			}
		}
		stale = append(stale, line)
	}
	var misplaced []string
	if release != "" {
		if doc.Pending > 0 {
			misplaced = append(misplaced, fmt.Sprintf("%d note(s) still under %q would publish under that heading; seal them with `just migration-notes-seal %s`", doc.Pending, unreleasedHeading, release))
		}
		for _, s := range doc.Sealed {
			if s != release {
				misplaced = append(misplaced, fmt.Sprintf("\"## %s\" has no published release and is not %s, so this release's page would not link it; rename it to \"## %s\"", s, release, release))
			}
		}
	}

	read := append([]string{unreleasedHeading}, doc.Sealed...)
	for i, s := range read[1:] {
		read[i+1] = "## " + s
	}
	fmt.Fprintf(w, "migration notes: %s under %s, against %s\n\n", notes, strings.Join(read, " and "), ref)
	fmt.Fprintf(w, "stable breaks: %d, %d without a note\n", stableN, len(missing))
	fmt.Fprintf(w, "unstable breaks: %d, %d with an optional note\n", unstableN, unstableNoted)
	fmt.Fprintf(w, "names that cover no break: %d\n", len(stale))
	if release != "" {
		fmt.Fprintf(w, "notes that would miss %s's page: %d\n", release, len(misplaced))
	}
	if len(missing) > 0 {
		sort.SliceStable(missing, func(i, j int) bool { return missing[i].Pkg < missing[j].Pkg })
		fmt.Fprintln(w, "\nstable breaks without a note:")
		for i, b := range missing {
			if i == 0 || missing[i-1].Pkg != b.Pkg {
				fmt.Fprintln(w, b.Pkg)
			}
			how := "changed"
			if b.Removed {
				how = "removed"
			}
			fmt.Fprintf(w, "    - %s (%s, %s)\n", b.Name, b.Kind, how)
		}
	}
	if len(stale) > 0 {
		fmt.Fprintln(w, "\nnames that cover no break:")
		for _, s := range stale {
			fmt.Fprintln(w, "    "+s)
		}
	}
	if len(misplaced) > 0 {
		fmt.Fprintf(w, "\nnotes that would miss %s's page:\n", release)
		for _, s := range misplaced {
			fmt.Fprintln(w, "    "+s)
		}
	}
	ok := len(missing) == 0 && len(stale) == 0 && len(misplaced) == 0
	switch {
	case ok:
		fmt.Fprintf(w, "\nevery stable break since %s has a note\n", ref)
	case len(missing)+len(stale) > 0:
		fmt.Fprintf(w, "\nWrite each missing note under %q in %s.\n", unreleasedHeading, notes)
		fmt.Fprintf(w, "A note's %s paragraph names a symbol, a type, or a package in\n", coversLabel)
		fmt.Fprintln(w, "backquotes. The page's \"Writing a note\" section gives the format.")
	}
	return ok, nil
}
