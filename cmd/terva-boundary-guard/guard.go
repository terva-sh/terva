package main

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// A baseline and how to read its growth. Each baseline file is meant to only
// shrink, and each test that reads one checks the tree against the file. A
// pull request can grow both together, and the test still passes. The guard
// closes that by comparing the file with its own past.
type baseline struct {
	path   string
	growth func(old, new string) ([]string, error)
}

var baselines = []baseline{
	{"packages/core/testdata/import_boundary_baseline.txt", importGrowth},
	{"packages/core/testdata/io_boundary_baseline.txt", ioGrowth},
}

// importEntry is one line of the import baseline:
// <unit> <dependency> <kind> <reason>.
type importEntry struct {
	unit, dep, kind string
}

func parseImport(text string) (map[[2]string]importEntry, error) {
	out := map[[2]string]importEntry{}
	for n, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Fields(line)
		if len(f) < 4 {
			return nil, fmt.Errorf("line %d: want \"<unit> <dependency> <kind> <reason>\", got %q", n+1, line)
		}
		e := importEntry{f[0], f[1], f[2]}
		switch e.kind {
		case "wire", "pure", "io":
		default:
			// A kind the guard does not know could be io under another
			// spelling. TestImportBoundary refuses it too.
			return nil, fmt.Errorf("line %d: kind %q is not wire, pure, or io", n+1, e.kind)
		}
		k := [2]string{e.unit, e.dep}
		if _, dup := out[k]; dup {
			return nil, fmt.Errorf("line %d: %s %s is listed twice", n+1, e.unit, e.dep)
		}
		out[k] = e
	}
	return out, nil
}

// importGrowth reports each io entry the import baseline gained: a new io
// line, or a line whose kind became io. A pure or wire line may be added: the
// baseline's own test says so, and TestIOBoundary scans what the engine
// reaches, so a leaf mislabelled pure fails there.
//
// An io package that moves reads as growth too, because its new path is a new
// line. Pairing it with the line it replaced would also excuse a different
// package that happens to share a name, and the baseline holds no io line to
// move. A real move carries the trailer.
func importGrowth(oldText, newText string) ([]string, error) {
	old, err := parseImport(oldText)
	if err != nil {
		return nil, fmt.Errorf("at the base: %w", err)
	}
	cur, err := parseImport(newText)
	if err != nil {
		return nil, err
	}
	var grew []string
	for _, k := range sortedKeys(cur) {
		e := cur[k]
		if e.kind != "io" {
			continue
		}
		o, ok := old[k]
		switch {
		case !ok:
			grew = append(grew, fmt.Sprintf("%s %s io was added", e.unit, e.dep))
		case o.kind != "io":
			grew = append(grew, fmt.Sprintf("%s %s changed kind from %s to io", e.unit, e.dep, o.kind))
		}
	}
	return grew, nil
}

// parseIO reads the I/O baseline, one "file<TAB>identifier<TAB>count" per
// line, into counts keyed by file and identifier.
func parseIO(text string) (map[[2]string]int, error) {
	out := map[[2]string]int{}
	for n, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Split(line, "\t")
		if len(f) != 3 || strings.TrimSpace(f[0]) == "" || strings.TrimSpace(f[1]) == "" {
			return nil, fmt.Errorf("line %d: want \"file ident count\", got %q", n+1, line)
		}
		c, err := strconv.Atoi(f[2])
		if err != nil || c < 0 {
			return nil, fmt.Errorf("line %d: bad count %q", n+1, f[2])
		}
		k := [2]string{f[0], f[1]}
		if _, dup := out[k]; dup {
			return nil, fmt.Errorf("line %d: %s %s is listed twice", n+1, f[0], f[1])
		}
		out[k] = c
	}
	return out, nil
}

// ioGrowth reports each file and identifier whose reference count rose. A file
// that moves is a new key, so it reads as growth, for the reason an io import
// that moves does: matching it to the file it came from would let a new call in
// one package cancel a removed call in another.
func ioGrowth(oldText, newText string) ([]string, error) {
	old, err := parseIO(oldText)
	if err != nil {
		return nil, fmt.Errorf("at the base: %w", err)
	}
	cur, err := parseIO(newText)
	if err != nil {
		return nil, err
	}
	var grew []string
	for _, k := range sortedKeys(cur) {
		if cur[k] > old[k] {
			grew = append(grew, fmt.Sprintf("%s %s rose from %d to %d references", k[0], k[1], old[k], cur[k]))
		}
	}
	return grew, nil
}

func sortedKeys[V any](m map[[2]string]V) [][2]string {
	keys := make([][2]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i][0] != keys[j][0] {
			return keys[i][0] < keys[j][0]
		}
		return keys[i][1] < keys[j][1]
	})
	return keys
}
