package lore

import (
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Result is the outcome of a Select: the entries that fired and fit the
// budget, bucketed for placement, plus those cut to fit. Callers route
// Constant entries into the cached prefix and the rest into the per-turn
// (uncached) tail — see the proposal's cache-split rule.
type Result struct {
	Before  []Entry // Position==before, ascending Order (placement order)
	After   []Entry // Position==after, ascending Order
	Dropped []Entry // fired but cut to fit the token budget (highest Order kept)
}

// All returns every activated entry (Before then After) in placement order.
func (r Result) All() []Entry {
	out := make([]Entry, 0, len(r.Before)+len(r.After))
	out = append(out, r.Before...)
	out = append(out, r.After...)
	return out
}

// ApproxTokens estimates a string's token cost the way terva does elsewhere
// (bytes/4) — terva ships no real tokenizer. Pass this (or a custom counter)
// to Select.
func ApproxTokens(s string) int { return len(s) / 4 }

// Select scans the recent conversation for entries whose triggers fire and
// returns them bucketed for placement, within the collection token budget.
//
// scan is the recent messages, most-recent first (index 0 == newest). Each
// message is treated as a unit: keys can't match across message boundaries
// (a \x01 separator blocks whole-word matches from bleeding between them,
// mirroring SillyTavern's WorldInfoBuffer). countTokens estimates budget
// cost; pass ApproxTokens for terva's default.
//
// The engine is pure and deterministic: no I/O, no globals, no clock.
func Select(entries []Entry, cfg Config, scan []string, countTokens func(string) int) Result {
	if len(entries) == 0 {
		return Result{}
	}
	depthDefault := cfg.ScanDepth
	if depthDefault <= 0 {
		depthDefault = DefaultScanDepth
	}

	// Activation with recursion. `order` preserves discovery order for a
	// stable tie-break; `on` dedups.
	on := make([]bool, len(entries))
	var activated []int
	var recurse strings.Builder

	for step := 1; step <= maxRecursionSteps; step++ {
		var newly []int
		for i := range entries {
			if on[i] {
				continue
			}
			// An entry marked exclude_recursion may only fire on the
			// initial pass, never on a recursion-triggered one.
			if step > 1 && entries[i].ExcludeRecursion {
				continue
			}
			if entryActivates(entries[i], cfg, scan, depthDefault, recurse.String()) {
				newly = append(newly, i)
			}
		}
		if len(newly) == 0 {
			break
		}
		for _, i := range newly {
			on[i] = true
			activated = append(activated, i)
		}
		if !cfg.RecursiveScanning {
			break
		}
		// Feed newly-activated content back into the scan buffer so it can
		// trigger further entries; entries marked prevent_recursion are
		// withheld. If nothing new is added, the fixpoint is reached.
		added := false
		for _, i := range newly {
			if entries[i].PreventRecursion {
				continue
			}
			recurse.WriteByte(0x01)
			recurse.WriteString(entries[i].Content)
			added = true
		}
		if !added {
			break
		}
	}

	// Budget: consider activated entries by descending Order (priority) and
	// include until the budget is exhausted. Mirrors SillyTavern: once an
	// entry overflows, it and all lower-priority entries are dropped. At
	// least the top-priority entry is always kept, so a misconfigured tiny
	// budget can't silently drop everything.
	sort.SliceStable(activated, func(a, b int) bool {
		return entries[activated[a]].Order > entries[activated[b]].Order
	})
	var kept, dropped []Entry
	overflowed, used := false, 0
	for _, i := range activated {
		e := entries[i]
		if overflowed {
			dropped = append(dropped, e)
			continue
		}
		if cfg.TokenBudget > 0 {
			cost := countTokens(e.Content)
			if len(kept) > 0 && used+cost > cfg.TokenBudget {
				overflowed = true
				dropped = append(dropped, e)
				continue
			}
			used += cost
		}
		kept = append(kept, e)
	}

	// Placement: bucket by position, ascending Order within each bucket
	// (highest-Order entry ends up nearest the character definition, as in
	// SillyTavern's final unshift ordering).
	var before, after []Entry
	for _, e := range kept {
		if e.Position == PositionBefore {
			before = append(before, e)
		} else {
			after = append(after, e)
		}
	}
	byAscOrder := func(s []Entry) {
		sort.SliceStable(s, func(a, b int) bool { return s[a].Order < s[b].Order })
	}
	byAscOrder(before)
	byAscOrder(after)
	return Result{Before: before, After: after, Dropped: dropped}
}

// entryActivates reports whether a single entry fires against the scan
// window (plus any recursion buffer).
func entryActivates(e Entry, cfg Config, scan []string, depthDefault int, recurse string) bool {
	if e.Constant {
		return true
	}
	depth := depthDefault
	if e.ScanDepth != nil && *e.ScanDepth > 0 {
		depth = *e.ScanDepth
	}
	// Case sensitivity: the collection default (cfg.CaseSensitive) sets the
	// floor and a per-entry case_sensitive:true opts an individual entry in —
	// mirroring MatchWholeWords, which is likewise a collection-level default.
	caseSensitive := e.CaseSensitive || cfg.CaseSensitive
	hay := buildHaystack(scan, depth, recurse, caseSensitive)
	if !anyKeyMatches(e.Keys, hay, caseSensitive, cfg.MatchWholeWords) {
		return false
	}
	if len(e.SecondaryKeys) == 0 {
		return true
	}
	return secondaryOK(e, hay, caseSensitive, cfg.MatchWholeWords)
}

// buildHaystack joins the newest `depth` messages (plus the recursion
// buffer) with \x01 separators, lowercasing unless the entry is
// case-sensitive.
func buildHaystack(scan []string, depth int, recurse string, caseSensitive bool) string {
	n := depth
	if n > len(scan) {
		n = len(scan)
	}
	var b strings.Builder
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteByte(0x01)
		}
		b.WriteString(scan[i])
	}
	if recurse != "" {
		b.WriteByte(0x01)
		b.WriteString(recurse)
	}
	s := b.String()
	if !caseSensitive {
		s = strings.ToLower(s)
	}
	return s
}

func anyKeyMatches(keys []string, hay string, caseSensitive, wholeWords bool) bool {
	for _, k := range keys {
		if containsKey(hay, normalizeNeedle(k, caseSensitive), wholeWords) {
			return true
		}
	}
	return false
}

// secondaryOK applies the entry's Logic across its secondary keys. The
// primary key has already matched by the time this is called.
func secondaryOK(e Entry, hay string, caseSensitive, wholeWords bool) bool {
	anyMatch, allMatch := false, true
	for _, k := range e.SecondaryKeys {
		if containsKey(hay, normalizeNeedle(k, caseSensitive), wholeWords) {
			anyMatch = true
		} else {
			allMatch = false
		}
	}
	switch e.Logic {
	case LogicAndAll:
		return allMatch
	case LogicNotAny:
		return !anyMatch
	case LogicNotAll:
		return !allMatch
	default: // LogicAndAny
		return anyMatch
	}
}

func normalizeNeedle(k string, caseSensitive bool) string {
	if caseSensitive {
		return k
	}
	return strings.ToLower(k)
}

// containsKey reports whether needle occurs in haystack. With wholeWords a
// single-token needle must sit on non-word boundaries; a multi-token needle
// falls back to substring (as SillyTavern does).
func containsKey(haystack, needle string, wholeWords bool) bool {
	if needle == "" {
		return false
	}
	if !wholeWords || strings.ContainsAny(needle, " \t") {
		return strings.Contains(haystack, needle)
	}
	from := 0
	for {
		i := strings.Index(haystack[from:], needle)
		if i < 0 {
			return false
		}
		i += from
		beforeOK := i == 0
		if !beforeOK {
			r, _ := utf8.DecodeLastRuneInString(haystack[:i])
			beforeOK = !isWordRune(r)
		}
		end := i + len(needle)
		afterOK := end == len(haystack)
		if !afterOK {
			r, _ := utf8.DecodeRuneInString(haystack[end:])
			afterOK = !isWordRune(r)
		}
		if beforeOK && afterOK {
			return true
		}
		from = i + 1
	}
}

func isWordRune(r rune) bool {
	return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r)
}
