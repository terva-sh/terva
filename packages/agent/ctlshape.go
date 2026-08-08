package agent

// Looking at a reply without reading it.
//
// This exists because of a specific incident. A `worlds.list` run to find one
// World's id returned every World in full, including a lorebook of private
// fiction, into a terminal and an agent's context — for a question whose answer
// was a fourteen-character string. The verb was not wrong and the reply was not
// wrong; asking for values when the question was about structure was.
//
// So `terva ctl --shape` prints the SHAPE: every path, its type, and how big it
// is. Not a redaction pass over the values — a redactor has to guess which
// fields are sensitive and it fails OPEN, quietly printing the one it did not
// think of. This never holds a value in the first place, so there is nothing to
// fail open about, and the property is testable as an absence: no input string
// may appear in the output.
//
// The sizes are the useful part beyond safety. "lore is an array of 9 whose
// content averages 488 bytes" is most of what a budget question ever needs, and
// it is the shape of answer `--select` is then aimed with.

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// shapeLines renders v as sorted "path  type" lines.
//
// Arrays are UNIONED rather than sampled: reporting the first element's keys
// would hide every field the first element happened to omit, which for a list of
// Worlds means a cover, a model, or a lorebook vanishing from the shape because
// object zero had none. The union is what the caller is deciding against.
func shapeLines(v any) []string {
	acc := map[string]*shapeNode{}
	walkShape("", v, acc)
	paths := make([]string, 0, len(acc))
	for p := range acc {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	out := make([]string, 0, len(paths))
	width := 0
	for _, p := range paths {
		if len(p) > width {
			width = len(p)
		}
	}
	for _, p := range paths {
		out = append(out, fmt.Sprintf("%-*s  %s", width, p, acc[p].describe()))
	}
	return out
}

// shapeNode accumulates what was seen at one path across every element of every
// array containing it.
type shapeNode struct {
	kinds   map[string]bool
	n       int // observations
	minSize int
	maxSize int
	total   int
	sized   int // observations that had a size (strings, arrays, objects)
}

func (s *shapeNode) observe(kind string, size int, hasSize bool) {
	if s.kinds == nil {
		s.kinds = map[string]bool{}
	}
	s.kinds[kind] = true
	s.n++
	if !hasSize {
		return
	}
	if s.sized == 0 || size < s.minSize {
		s.minSize = size
	}
	if size > s.maxSize {
		s.maxSize = size
	}
	s.total += size
	s.sized++
}

func (s *shapeNode) describe() string {
	kinds := make([]string, 0, len(s.kinds))
	for k := range s.kinds {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)
	// A path that is a string in one element and null in another is a fact worth
	// printing rather than smoothing over: it is usually an omitempty field, and
	// "sometimes absent" changes how a caller writes the selector.
	d := strings.Join(kinds, "|")
	if s.sized == 0 {
		if s.n > 1 {
			return fmt.Sprintf("%s ×%d", d, s.n)
		}
		return d
	}
	if s.minSize == s.maxSize {
		d += fmt.Sprintf("(%d)", s.maxSize)
	} else {
		d += fmt.Sprintf("(%d..%d, avg %d)", s.minSize, s.maxSize, s.total/s.sized)
	}
	if s.n > 1 {
		d += fmt.Sprintf(" ×%d", s.n)
	}
	return d
}

func walkShape(path string, v any, acc map[string]*shapeNode) {
	node := acc[path]
	if node == nil {
		node = &shapeNode{}
		acc[path] = node
	}
	switch t := v.(type) {
	case nil:
		node.observe("null", 0, false)
	case bool:
		node.observe("bool", 0, false)
	case float64:
		node.observe("number", 0, false)
	case string:
		// The LENGTH, never the text. This is the line the whole file exists for.
		node.observe("string", len(t), true)
	case []any:
		node.observe("array", len(t), true)
		child := path + "[]"
		if len(t) == 0 {
			// An empty array still has a path worth printing, or "lore: array(0)"
			// reads as though the shape below it is unknown rather than absent.
			return
		}
		for _, e := range t {
			walkShape(child, e, acc)
		}
	case map[string]any:
		node.observe("object", len(t), true)
		for k, e := range t {
			child := k
			if path != "" {
				child = path + "." + k
			}
			walkShape(child, e, acc)
		}
	default:
		node.observe(fmt.Sprintf("%T", v), 0, false)
	}
}

// selectPaths narrows a decoded reply to the given dotted paths.
//
// A path walks objects by key and MAPS over arrays: `worlds.name` on a list
// answers for every World, because that is what someone asking for it means.
//
// Paths that cross the SAME array come back as ROWS — one object per element,
// each carrying every selected field of that element:
//
//	--select worlds.id,worlds.name
//	{"worlds": [{"id": "emma-825e…", "name": "Emma"}, …]}
//
// rather than one array per path. Parallel arrays were the first shape and they
// are a trap: a field absent from some elements produces a SHORTER array, so
// `sessions.world` came back with two entries against fourteen sessions and read
// as "the first two". Nothing said otherwise, and the numbers looked fine. A row
// carries its own association, so a missing field is simply a key the row does
// not have, and there is no alignment left to get wrong.
//
// Rows group by the FIRST array a path crosses. A scalar path that crosses none
// stays a plain top-level key.
//
// A path that matches nothing is an ERROR rather than an empty result. The
// failure it prevents is the quiet one: a typo returning `{}` looks exactly like
// a daemon that answered with nothing, and the caller reaches the wrong
// conclusion about their data instead of about their spelling.
func selectPaths(v any, paths []string) (map[string]any, error) {
	type rowSet struct {
		elems []any    // the array's elements, in order
		keys  []string // the remainder of each path below the array
		names []string // what to call each in the row
	}
	out := map[string]any{}
	rows := map[string]*rowSet{}
	var order []string

	for _, p := range paths {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		parts := strings.Split(p, ".")
		arrPath, elems, rest := firstArray(v, parts)
		if elems == nil {
			// No array on the way down — a plain value at a plain key.
			got, ok := pluck(v, parts)
			if !ok {
				return nil, fmt.Errorf("no such path %q in the reply (try --shape to see what is there)", p)
			}
			out[p] = got
			continue
		}
		// Prove the path resolves in at least one element before accepting it —
		// a typo below the array would otherwise yield rows that are all missing
		// the same key, which reads as "the data does not have it".
		found := false
		for _, e := range elems {
			if _, ok := pluck(e, rest); ok {
				found = true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("no such path %q in the reply (try --shape to see what is there)", p)
		}
		rs := rows[arrPath]
		if rs == nil {
			rs = &rowSet{elems: elems}
			rows[arrPath] = rs
			order = append(order, arrPath)
		}
		rs.keys = append(rs.keys, strings.Join(rest, "."))
		rs.names = append(rs.names, strings.Join(rest, "."))
	}
	if len(out) == 0 && len(order) == 0 {
		return nil, fmt.Errorf("--select needs at least one path")
	}
	for _, arrPath := range order {
		rs := rows[arrPath]
		built := make([]any, 0, len(rs.elems))
		for _, e := range rs.elems {
			row := map[string]any{}
			for i, k := range rs.keys {
				if got, ok := pluck(e, strings.Split(k, ".")); ok {
					row[rs.names[i]] = got
				}
			}
			built = append(built, row)
		}
		out[arrPath] = built
	}
	return out, nil
}

// firstArray walks parts until the value under it is an array, and reports the
// dotted prefix that named it, its elements, and the remaining path below it.
// elems is nil when the whole path resolves without crossing one.
func firstArray(v any, parts []string) (prefix string, elems []any, rest []string) {
	cur := v
	for i, part := range parts {
		m, ok := cur.(map[string]any)
		if !ok {
			return "", nil, nil
		}
		next, ok := m[part]
		if !ok {
			return "", nil, nil
		}
		if arr, isArr := next.([]any); isArr {
			return strings.Join(parts[:i+1], "."), arr, parts[i+1:]
		}
		cur = next
	}
	return "", nil, nil
}

// pluck resolves a path within one value. It still MAPS over arrays, which is
// how a path below the row array (a World's `lore.name`) answers with all of
// them — the row grouping above only handles the FIRST array crossed.
//
// An element that lacks the path is skipped rather than failing: within a row
// set that is an absent key on that row, which is a fact the row can carry.
func pluck(v any, parts []string) (any, bool) {
	if len(parts) == 0 {
		return v, true
	}
	switch t := v.(type) {
	case map[string]any:
		next, ok := t[parts[0]]
		if !ok {
			return nil, false
		}
		return pluck(next, parts[1:])
	case []any:
		out := make([]any, 0, len(t))
		hit := false
		for _, e := range t {
			if got, ok := pluck(e, parts); ok {
				out = append(out, got)
				hit = true
			}
		}
		return out, hit
	default:
		return nil, false
	}
}

// renderJSON is the full-value output, pretty-printed and newline-terminated.
func renderJSON(v any) (string, error) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return "", err
	}
	return string(b) + "\n", nil
}
