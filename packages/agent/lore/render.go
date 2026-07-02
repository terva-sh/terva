package lore

import "strings"

// Partition splits entries into always-on (constant) and keyword-triggered
// sets. Constant entries are placed in the cached system-prompt prefix;
// triggered entries are scanned and injected into the per-turn tail.
func Partition(entries []Entry) (constant, triggered []Entry) {
	for _, e := range entries {
		if e.Constant {
			constant = append(constant, e)
		} else {
			triggered = append(triggered, e)
		}
	}
	return constant, triggered
}

// Render formats entries into a single <lore> context block, or "" when
// there are none. Entries appear in the given order, blank-line separated.
// The wrapper marks the content as reference knowledge the model may draw
// on, distinct from the conversation itself.
func Render(entries []Entry) string {
	if len(entries) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("<lore>\n")
	for i, e := range entries {
		if i > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString(strings.TrimSpace(e.Content))
	}
	b.WriteString("\n</lore>")
	return b.String()
}

// PlaceConstant orders always-on entries for the cached prefix (before-
// then after-position, ascending Order within each) and renders them. It
// applies no token budget — constant entries are always present by author
// intent. Returns "" when there are none.
func PlaceConstant(constant []Entry) string {
	if len(constant) == 0 {
		return ""
	}
	// Reuse the placement logic with an empty scan: constant entries fire
	// unconditionally, and a zero Config imposes no budget.
	return Render(Select(constant, Config{}, nil, ApproxTokens).All())
}
