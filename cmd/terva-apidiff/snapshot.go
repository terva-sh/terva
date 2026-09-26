package main

// Snapshot mode: the committed record of the engine's exported API.
//
// The release census above answers one question at a cut: did the surface
// break since the last release? It keeps no record between cuts, and it walks
// each directory recursively, so a subpackage's symbols land in its parent's
// namespace. Snapshot mode is the record. A manifest (.api/packages.txt) marks
// each package stable or unstable, and one file per package lists its exported
// symbols. `-check` fails when a file no longer matches the code, so an API
// change always arrives as a diff a reviewer reads. `-since` counts what
// changed against a ref, per class, and never fails: before 1.0 anything may
// change, and the point is that it is measured (TKT-01M39VEJD8).
//
// 🔑 One directory is one package. The census here never descends, so
// packages/core and packages/core/stall are two snapshots and two namespaces,
// and a New in one cannot stand in for a New removed from the other.

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	pathpkg "path"
	"path/filepath"
	"sort"
	"strings"
)

const (
	classStable   = "stable"
	classUnstable = "unstable"

	manifestName = "packages.txt"
)

// entry is one line of the manifest: a package directory and its class.
type entry struct {
	Pkg   string
	Class string
}

// readManifest reads dir/packages.txt: one "class package" pair per line,
// with # comments. An empty manifest is an error for the same reason an empty
// census is one: a gate over nothing reports clean.
func readManifest(dir string) ([]entry, error) {
	data, err := os.ReadFile(filepath.Join(dir, manifestName))
	if err != nil {
		return nil, err
	}
	return parseManifest(data)
}

// readManifestAtRef reads the manifest as it was at ref, or returns nil when
// ref predates it.
func readManifestAtRef(repo, ref, dir string) ([]entry, error) {
	rel, err := filepath.Rel(repo, filepath.Join(dir, manifestName))
	if err != nil {
		return nil, err
	}
	spec := ref + ":" + filepath.ToSlash(rel)
	if _, err := run(repo, "cat-file", "-e", spec); err != nil {
		return nil, nil
	}
	data, err := runBytes(repo, "show", spec)
	if err != nil {
		return nil, err
	}
	entries, err := parseManifest(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", spec, err)
	}
	return entries, nil
}

func parseManifest(data []byte) ([]entry, error) {
	var out []entry
	seen := map[string]bool{}
	sc := bufio.NewScanner(bytes.NewReader(data))
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 {
			return nil, fmt.Errorf("%s:%d: want \"<class> <package>\", got %q", manifestName, n, line)
		}
		class, pkg := fields[0], strings.TrimSuffix(fields[1], "/")
		if class != classStable && class != classUnstable {
			return nil, fmt.Errorf("%s:%d: class %q is neither %s nor %s", manifestName, n, class, classStable, classUnstable)
		}
		if seen[pkg] {
			return nil, fmt.Errorf("%s:%d: %s is listed twice", manifestName, n, pkg)
		}
		seen[pkg] = true
		out = append(out, entry{Pkg: pkg, Class: class})
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%s lists no package; a snapshot of nothing is not a clean report", manifestName)
	}
	return out, nil
}

// snapshotFile is where pkg's snapshot lives under dir.
func snapshotFile(dir, pkg string) string {
	return filepath.Join(dir, filepath.FromSlash(pkg)+".txt")
}

// dirFiles reads the Go sources directly in pkg, in the working tree. It does
// not descend: a subdirectory is another package.
func dirFiles(repo, pkg string) (map[string][]byte, error) {
	dir := filepath.Join(repo, filepath.FromSlash(pkg))
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	files := map[string][]byte{}
	for _, e := range ents {
		rel := pkg + "/" + e.Name()
		if e.IsDir() || !isSource(rel) {
			continue
		}
		body, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		files[rel] = body
	}
	return files, nil
}

// dirFilesAtRef reads the Go sources directly in pkg at a git ref. A package
// that did not exist there yields no files and no error; the caller reports it
// as new.
func dirFilesAtRef(repo, ref, pkg string) (map[string][]byte, error) {
	// Without -r, ls-tree lists the directory's own entries, so a
	// subpackage shows up as a tree and is skipped.
	out, err := run(repo, "ls-tree", "-z", ref, "--", pkg+"/")
	if err != nil {
		return nil, err
	}
	files := map[string][]byte{}
	for _, rec := range strings.Split(out, "\x00") {
		meta, path, found := strings.Cut(rec, "\t")
		if !found || !isSource(path) {
			continue
		}
		fields := strings.Fields(meta)
		if len(fields) < 3 || fields[1] != "blob" {
			continue
		}
		blob, err := runBytes(repo, "cat-file", "blob", fields[2])
		if err != nil {
			return nil, err
		}
		files[path] = blob
	}
	return files, nil
}

// censusPkg censuses one package directory in the working tree, refusing a
// census of nothing.
func censusPkg(repo, pkg string) (map[string]symbol, error) {
	files, err := dirFiles(repo, pkg)
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("%s: no Go source; drop it from %s or fix the path", pkg, manifestName)
	}
	syms, err := census(files)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", pkg, err)
	}
	if len(syms) == 0 {
		return nil, fmt.Errorf("%s: %d file(s) and not one exported symbol; a census of nothing is not a clean report", pkg, len(files))
	}
	return syms, nil
}

// formatSnapshot renders a snapshot: a header naming the package and its
// class, then one "name<TAB>kind<TAB>signature" line per symbol, sorted. A
// rendered signature never holds a tab, because render joins on spaces. A
// symbol with no signature (a const) drops the last field, so no line ends in
// whitespace. A symbol marked Unstable: has " unstable" after its kind, so a
// new marker shows in the snapshot's diff where a reviewer reads it.
func formatSnapshot(e entry, syms map[string]symbol) []byte {
	var b bytes.Buffer
	fmt.Fprintf(&b, "# %s (%s): its exported API, one symbol per line.\n", e.Pkg, e.Class)
	b.WriteString("# Written by `just api-snapshot`. `just api-check` fails when it no longer matches the code.\n")
	for _, s := range sortedSymbols(syms) {
		kind := s.Kind
		if s.Unstable {
			kind += " " + classUnstable
		}
		if s.Sig == "" {
			fmt.Fprintf(&b, "%s\t%s\n", s.Name, kind)
			continue
		}
		fmt.Fprintf(&b, "%s\t%s\t%s\n", s.Name, kind, s.Sig)
	}
	return b.Bytes()
}

// parseSnapshot reads the symbol lines back, ignoring the header.
func parseSnapshot(data []byte) (map[string]symbol, error) {
	syms := map[string]symbol{}
	for n, line := range strings.Split(string(data), "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "\t", 3)
		if len(parts) < 2 {
			return nil, fmt.Errorf("line %d: want a name, a kind and a signature separated by tabs", n+1)
		}
		s := symbol{Name: parts[0], Kind: parts[1]}
		if kind, ok := strings.CutSuffix(s.Kind, " "+classUnstable); ok {
			s.Kind, s.Unstable = kind, true
		}
		if len(parts) == 3 {
			s.Sig = parts[2]
		}
		syms[s.Name] = s
	}
	return syms, nil
}

func sortedSymbols(syms map[string]symbol) []symbol {
	out := make([]symbol, 0, len(syms))
	for _, s := range syms {
		out = append(out, s)
	}
	sortSymbols(out)
	return out
}

// diffSymbols compares two censuses the way comparePkg does.
func diffSymbols(pkg string, base, head map[string]symbol) pkgReport {
	r := pkgReport{Pkg: pkg, BaseSyms: len(base), HeadSyms: len(head)}
	for name, s := range head {
		prev, ok := base[name]
		switch {
		case !ok:
			r.Added = append(r.Added, s)
		case prev.Sig != s.Sig || prev.Kind != s.Kind:
			r.Changed = append(r.Changed, change{Symbol: s, Was: prev.Sig, WasKind: prev.Kind, WasUnstable: prev.Unstable})
		}
	}
	for name, s := range base {
		if _, ok := head[name]; !ok {
			r.Removed = append(r.Removed, s)
		}
	}
	sortSymbols(r.Added)
	sortSymbols(r.Removed)
	sort.Slice(r.Changed, func(i, j int) bool { return r.Changed[i].Symbol.Name < r.Changed[j].Symbol.Name })
	return r
}

// orphans lists the snapshot files under dir that no manifest entry owns. A
// package dropped from the manifest must take its snapshot with it, or the
// directory keeps describing an API nothing checks.
func orphans(dir string, entries []entry) ([]string, error) {
	owned := map[string]bool{}
	for _, e := range entries {
		owned[filepath.Clean(snapshotFile(dir, e.Pkg))] = true
	}
	var out []string
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".txt") || filepath.Base(path) == manifestName && filepath.Dir(path) == filepath.Clean(dir) {
			return nil
		}
		if !owned[filepath.Clean(path)] {
			out = append(out, path)
		}
		return nil
	})
	sort.Strings(out)
	return out, err
}

// unlisted returns every package under a listed one that the manifest does not
// list. Listing packages/core claims the tree below it, so a new component
// package there cannot join the API unmeasured. An exp, internal or testdata
// directory holds nothing a host can depend on, so none of them counts.
func unlisted(repo string, entries []entry) ([]string, error) {
	listed := map[string]bool{}
	for _, e := range entries {
		listed[e.Pkg] = true
	}
	found := map[string]bool{}
	for _, e := range entries {
		root := filepath.Join(repo, filepath.FromSlash(e.Pkg))
		err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				switch d.Name() {
				case expDir, "internal", "testdata":
					return filepath.SkipDir
				}
				return nil
			}
			rel, err := filepath.Rel(repo, path)
			if err != nil {
				return err
			}
			rel = filepath.ToSlash(rel)
			if pkg := filepath.ToSlash(filepath.Dir(rel)); isSource(rel) && !listed[pkg] {
				found[pkg] = true
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	out := make([]string, 0, len(found))
	for pkg := range found {
		out = append(out, pkg)
	}
	sort.Strings(out)
	return out, nil
}

// writeSnapshots rewrites every snapshot from the code and removes orphans.
func writeSnapshots(repo, dir string, w io.Writer) error {
	entries, err := readManifest(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		syms, err := censusPkg(repo, e.Pkg)
		if err != nil {
			return err
		}
		path := snapshotFile(dir, e.Pkg)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(path, formatSnapshot(e, syms), 0o644); err != nil {
			return err
		}
		fmt.Fprintf(w, "%s (%s): %d symbols\n", e.Pkg, e.Class, len(syms))
	}
	stale, err := orphans(dir, entries)
	if err != nil {
		return err
	}
	for _, path := range stale {
		if err := os.Remove(path); err != nil {
			return err
		}
		fmt.Fprintf(w, "removed %s: no manifest entry owns it\n", path)
	}
	missing, err := unlisted(repo, entries)
	if err != nil {
		return err
	}
	for _, pkg := range missing {
		fmt.Fprintf(w, "%s: not in %s, so it has no snapshot; list it as stable or unstable\n", pkg, manifestName)
	}
	return nil
}

// checkSnapshots reports every snapshot that no longer matches the code, and
// returns false when any does. The comparison is byte for byte, so a changed
// class or header counts too; the symbol diff is the explanation.
func checkSnapshots(repo, dir string, w io.Writer) (bool, error) {
	entries, err := readManifest(dir)
	if err != nil {
		return false, err
	}
	ok := true
	censuses := map[string]map[string]symbol{}
	for _, e := range entries {
		syms, err := censusPkg(repo, e.Pkg)
		if err != nil {
			return false, err
		}
		censuses[e.Pkg] = syms
		for _, name := range untypedVars(syms) {
			ok = false
			fmt.Fprintf(w, "%s: var %s does not spell its type, so a change to it would reach no snapshot; write `var %s T = ...`\n", e.Pkg, name, name)
		}
		want := formatSnapshot(e, syms)
		path := snapshotFile(dir, e.Pkg)
		have, err := os.ReadFile(path)
		if os.IsNotExist(err) {
			ok = false
			fmt.Fprintf(w, "%s (%s): no snapshot at %s\n", e.Pkg, e.Class, path)
			continue
		}
		if err != nil {
			return false, err
		}
		if bytes.Equal(have, want) {
			continue
		}
		ok = false
		recorded, err := parseSnapshot(have)
		if err != nil {
			return false, fmt.Errorf("%s: %w", path, err)
		}
		r := diffSymbols(e.Pkg, recorded, syms)
		fmt.Fprintf(w, "%s (%s): the snapshot is out of date\n", e.Pkg, e.Class)
		printChanges(w, r)
		remarked := printMarkerChanges(w, recorded, syms)
		if len(r.Added)+len(r.Removed)+len(r.Changed)+remarked == 0 {
			fmt.Fprintln(w, "    the symbols match; the header or the format differs")
		}
	}
	module, err := modulePath(repo)
	if err != nil {
		return false, err
	}
	leaks, followed := closure(module, entries, censuses)
	for _, leak := range leaks {
		fmt.Fprintln(w, leak)
	}
	if len(leaks) > 0 {
		fmt.Fprintln(w, "A stable symbol promises the types it names. Mark it Unstable: too, or make what it names stable.")
	}
	stale, err := orphans(dir, entries)
	if err != nil {
		return false, err
	}
	for _, path := range stale {
		ok = false
		fmt.Fprintf(w, "%s: no manifest entry owns this snapshot\n", path)
	}
	missing, err := unlisted(repo, entries)
	if err != nil {
		return false, err
	}
	for _, pkg := range missing {
		ok = false
		fmt.Fprintf(w, "%s: not in %s; list it as stable or unstable, then run `just api-snapshot`\n", pkg, manifestName)
	}
	differs := !ok
	ok = ok && len(leaks) == 0
	if ok {
		// 🔑 A clean check says so. A silent pass reads the same in a CI log
		// as a step that never ran, and the first CI run of this gate was
		// exactly that: an empty log at 0s.
		fmt.Fprintf(w, "%d packages match their snapshots in %s\n", len(entries), dir)
		fmt.Fprintf(w, "no stable signature names an unstable symbol (%d references followed)\n", followed)
	}
	if differs {
		fmt.Fprintln(w, "\nThe exported API differs from its committed record. If the change is meant,")
		fmt.Fprintln(w, "run `just api-snapshot` and commit the result: the diff under .api/ is how a")
		fmt.Fprintln(w, "reviewer sees an API change. Nothing here judges compatibility; `just api-since`")
		fmt.Fprintln(w, "counts the breaks.")
	}
	return ok, nil
}

// sincePkg is one package's diff between a ref and the working tree.
type sincePkg struct {
	Pkg string
	// Now is the package's class in the manifest now, and Then its class at
	// the ref. Then decides the class its breaks count in.
	Now, Then string
	// New is a package with no source at the ref, Gone one with none now.
	New, Gone  bool
	Base, Head map[string]symbol
	Diff       pkgReport
}

// diffSince diffs every manifest package against ref, then every package gone
// since ref, in that order.
func diffSince(repo, dir, ref string) ([]sincePkg, error) {
	entries, err := readManifest(dir)
	if err != nil {
		return nil, err
	}
	// The manifest at ref decides each package's class for the count: a
	// break to a package that was stable then is a stable break, even if the
	// package is unstable now.
	baseEntries, err := readManifestAtRef(repo, ref, dir)
	if err != nil {
		return nil, err
	}
	var out []sincePkg
	for _, e := range entries {
		head, err := censusPkg(repo, e.Pkg)
		if err != nil {
			return nil, err
		}
		baseFiles, err := dirFilesAtRef(repo, ref, e.Pkg)
		if err != nil {
			return nil, err
		}
		base, err := census(baseFiles)
		if err != nil {
			return nil, fmt.Errorf("%s at %s: %w", e.Pkg, ref, err)
		}
		r := diffSymbols(e.Pkg, base, head)
		r.Changed = knownBefore(r.Changed)
		then := e.Class
		if was := classAt(baseEntries, e.Pkg); was != "" && len(baseFiles) > 0 {
			then = was
		}
		out = append(out, sincePkg{Pkg: e.Pkg, Now: e.Class, Then: then,
			New: len(baseFiles) == 0, Base: base, Head: head, Diff: r})
	}
	// A package deleted since ref has no manifest entry left to walk, and
	// its removals would vanish from the count. Find them at ref instead.
	gone, err := goneSince(repo, ref, entries, baseEntries)
	if err != nil {
		return nil, err
	}
	for _, g := range gone {
		baseFiles, err := dirFilesAtRef(repo, ref, g.Pkg)
		if err != nil {
			return nil, err
		}
		base, err := census(baseFiles)
		if err != nil {
			return nil, fmt.Errorf("%s at %s: %w", g.Pkg, ref, err)
		}
		head := map[string]symbol{}
		out = append(out, sincePkg{Pkg: g.Pkg, Now: g.Class, Then: g.Class,
			Gone: true, Base: base, Head: head, Diff: diffSymbols(g.Pkg, base, head)})
	}
	return out, nil
}

// breakClass is the class a removed or changed symbol breaks in: its
// package's class at the ref, unless a marker there took the symbol out of
// that promise.
func breakClass(pkgClass string, marked bool) string {
	if marked {
		return classUnstable
	}
	return pkgClass
}

// sinceReport counts the changes to every manifest package between ref and
// the working tree, per class. It measures and never judges: the caller exits
// zero whatever it finds.
func sinceReport(repo, dir, ref string, w io.Writer) error {
	pkgs, err := diffSince(repo, dir, ref)
	if err != nil {
		return err
	}
	type tally struct{ removed, changed, added, pkgs int }
	totals := map[string]*tally{classStable: {}, classUnstable: {}}
	// count adds one package's diff to the totals, symbol by symbol. A
	// removal or a change takes the class the symbol had at ref. An addition
	// has no class at ref, so it takes the class it has now. A package counts
	// once in each class it adds to, and always in its class at ref.
	var marksNow, marksThen int
	count := func(then, now string, r pkgReport) (stable pkgReport) {
		touched := map[string]bool{then: true}
		for _, s := range r.Removed {
			c := breakClass(then, s.Unstable)
			touched[c] = true
			totals[c].removed++
			if c == classStable {
				stable.Removed = append(stable.Removed, s)
			}
		}
		for _, ch := range r.Changed {
			c := breakClass(then, ch.WasUnstable)
			touched[c] = true
			totals[c].changed++
			if c == classStable {
				stable.Changed = append(stable.Changed, ch)
			}
		}
		for _, s := range r.Added {
			c := breakClass(now, s.Unstable)
			touched[c] = true
			totals[c].added++
		}
		for c := range touched {
			totals[c].pkgs++
		}
		return stable
	}
	fmt.Fprintf(w, "exported API: %s -> the working tree, by class\n\n", ref)
	for _, p := range pkgs {
		r := p.Diff
		stable := count(p.Then, p.Now, r)
		if p.Gone {
			if p.Then == classStable {
				marksThen += markedCount(p.Base)
			}
			fmt.Fprintf(w, "%s (%s, gone since %s): %d removed\n", p.Pkg, p.Then, ref, len(r.Removed))
			printChanges(w, stable)
			continue
		}
		note := ""
		switch {
		case p.New:
			note = ", new since " + ref
		case p.Then != p.Now:
			note = ", " + p.Then + " at " + ref + " and counted so"
		}
		marks := ""
		if p.Then == classStable || p.Now == classStable {
			now, then := markedCount(p.Head), markedCount(p.Base)
			// Each total counts the packages that were stable when it was
			// taken: the manifest now for the markers now, the manifest at
			// ref for the markers then.
			if p.Now == classStable {
				marksNow += now
			}
			if p.Then == classStable {
				marksThen += then
			}
			marks = fmt.Sprintf(", %d marked unstable, %d at %s", now, then, ref)
			// In a package unstable at ref every break is unstable, and none
			// of them is the marker's doing.
			if n := len(r.Removed) + len(r.Changed) - len(stable.Removed) - len(stable.Changed); p.Then == classStable && n > 0 {
				marks += fmt.Sprintf("; %d of the breaks to marked symbols", n)
			}
		}
		fmt.Fprintf(w, "%s (%s%s): %d removed, %d changed, %d added (%d symbols%s)\n",
			p.Pkg, p.Now, note, len(r.Removed), len(r.Changed), len(r.Added), len(p.Head), marks)
		printChanges(w, stable)
	}
	fmt.Fprintln(w)
	for _, class := range []string{classStable, classUnstable} {
		t := totals[class]
		fmt.Fprintf(w, "%s: %d package(s), %d break(s) (%d removed, %d changed), %d added\n",
			class, t.pkgs, t.removed+t.changed, t.removed, t.changed, t.added)
	}
	fmt.Fprintf(w, "marked unstable in the stable packages: %d symbol(s), %d at %s\n", marksNow, marksThen, ref)
	fmt.Fprintln(w, "The unstable count takes in each symbol marked Unstable: in a stable package,")
	fmt.Fprintln(w, "and each package whose diff touches one.")
	fmt.Fprintln(w, "\nA measurement, not a verdict. Before 1.0 a stable package may still break;")
	fmt.Fprintln(w, "this is the count of how often it did.")
	return nil
}

// knownBefore drops the changes to a var whose type the census could not see
// at the ref. A type that became visible is not a break: the check now makes
// every var spell its type, and an older ref predates that rule.
func knownBefore(changed []change) []change {
	out := changed[:0]
	for _, c := range changed {
		if c.Symbol.Kind == "var" && c.WasKind == "var" && c.Was == "" {
			continue
		}
		out = append(out, c)
	}
	return out
}

// untypedVars names the exported vars whose type the census cannot see. The
// census reads syntax, so `var X = f()` records no type, and a change to f's
// result would pass the check. The check refuses them rather than claim a
// coverage it lacks, and one spelled-out type fixes each.
func untypedVars(syms map[string]symbol) []string {
	var out []string
	for _, s := range syms {
		if s.Kind == "var" && s.Sig == "" {
			out = append(out, s.Name)
		}
	}
	sort.Strings(out)
	return out
}

// goneSince lists the packages that existed at ref under a listed package and
// no longer exist. A package that still exists but is unlisted is not gone:
// `-check` reports that one.
//
// base is the manifest at ref, nil when ref predates it. It supplies both the
// trees to search and the class: a deleted package takes its entry's own class
// there, or its nearest listed ancestor's. Only a ref with no manifest falls
// back to the current one, where a deleted top-level entry has no owner and a
// deleted unstable package inherits its parent's class.
func goneSince(repo, ref string, entries, base []entry) ([]entry, error) {
	listed := map[string]bool{}
	for _, e := range entries {
		listed[e.Pkg] = true
	}
	claims := base
	if claims == nil {
		claims = entries
	}
	seen := map[string]bool{}
	var out []entry
	for _, e := range claims {
		list, err := run(repo, "ls-tree", "-r", "-z", "--name-only", ref, "--", e.Pkg+"/")
		if err != nil {
			return nil, err
		}
		for _, path := range strings.Split(list, "\x00") {
			pkg := pathpkg.Dir(path)
			if seen[pkg] || !isSource(path) || listed[pkg] || hiddenPkg(pkg) {
				continue
			}
			seen[pkg] = true
			// Gone means no Go source left. A directory that keeps only a
			// README is still a deleted package.
			if files, err := dirFiles(repo, pkg); err == nil && len(files) > 0 {
				continue
			}
			out = append(out, entry{Pkg: pkg, Class: classAt(claims, pkg)})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Pkg < out[j].Pkg })
	return out, nil
}

// classAt is pkg's class under entries: its own entry's, or its nearest
// listed ancestor's, or "" when nothing claims it.
func classAt(entries []entry, pkg string) string {
	class := map[string]string{}
	for _, e := range entries {
		class[e.Pkg] = e.Class
	}
	for owner := pkg; owner != "." && owner != "/"; owner = pathpkg.Dir(owner) {
		if c := class[owner]; c != "" {
			return c
		}
	}
	return ""
}

// hiddenPkg reports whether a package sits in a directory no host can depend
// on: internal (Go forbids the import) or testdata (Go ignores it). exp is
// already outside isSource.
func hiddenPkg(pkg string) bool {
	for _, seg := range strings.Split(pkg, "/") {
		if seg == "internal" || seg == "testdata" {
			return true
		}
	}
	return false
}

func printChanges(w io.Writer, r pkgReport) {
	for _, s := range r.Removed {
		fmt.Fprintf(w, "    - %s (%s)\n", s.Name, s.Kind)
	}
	for _, c := range r.Changed {
		if c.WasKind != "" && c.WasKind != c.Symbol.Kind {
			fmt.Fprintf(w, "    ~ %s: %s %s -> %s %s\n", c.Symbol.Name, c.WasKind, c.Was, c.Symbol.Kind, c.Symbol.Sig)
			continue
		}
		fmt.Fprintf(w, "    ~ %s: %s -> %s\n", c.Symbol.Name, c.Was, c.Symbol.Sig)
	}
	for _, s := range r.Added {
		fmt.Fprintf(w, "    + %s (%s)\n", s.Name, s.Kind)
	}
}
