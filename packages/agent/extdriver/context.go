package extdriver

import (
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"terva.sh/terva/packages/i18n"
)

// Host context contributions (protocol 2). Extensions supply content;
// the host (here) wraps, attributes, bounds, and orders it. The agent
// pulls EphemeralContext() once per turn for the model-context tail, the
// system-prompt build pulls StaticContext() for the cached addendum, and
// the TUI pulls StatusSegments() for the status line.

// contextCard is one dynamic card an extension set via context_card.
// blocking marks open work: the host's at-close gate re-prompts the
// model once when it tries to finish while such a card is present (see
// Driver.HasBlockingContext). The card text itself is injected
// normally every turn — blocking adds the gate, nothing to the card.
type contextCard struct {
	label    string
	text     string
	priority int
	blocking bool
}

// Byte budgets. Per-card and per-static caps keep one extension from
// flooding the model; the total ephemeral cap bounds the uncached
// per-turn tail across all extensions.
const (
	// maxStaticContextBytes bounds one extension's static system-prompt
	// block. Larger than the early limit so a memory-style extension can
	// carry a few KB of standing notes (refresh_context, protocol 3)
	// while the host still trims any one extension that floods the cached
	// prefix.
	maxStaticContextBytes = 8192
	maxCardBytes          = 4096
	maxEphemeralBytes     = 8192
)

// SetContextDisabled records which extensions are opted out of
// contributing model context (from the resolved user ∪ project config).
// Their tools, commands, and status segments still work; only their
// static contributions and cards are ignored. Replaces the set
// wholesale so concurrent readers that captured the old reference are
// unaffected.
func (d *Driver) SetContextDisabled(names []string) {
	set := make(map[string]bool, len(names))
	for _, n := range names {
		if n != "" {
			set[n] = true
		}
	}
	d.mu.Lock()
	d.contextDisabled = set
	d.mu.Unlock()
}

// contextDisabledSet returns the current disabled set under the lock.
// The returned map is never mutated after assignment, so the caller may
// read it without holding the lock.
func (d *Driver) contextDisabledSet() map[string]bool {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.contextDisabled
}

// StaticContext returns every (context-enabled) extension's static
// contribution, each host-wrapped and attributed, joined for folding
// into the cached system-prompt addendum. Deterministic (sorted by
// extension name). Empty when no extension contributed.
func (d *Driver) StaticContext() string {
	exts := d.snapshotExts()
	disabled := d.contextDisabledSet()
	var blocks []string
	for _, ext := range exts {
		if disabled[ext.Manifest.Name] {
			continue
		}
		ext.mu.Lock()
		txt := ext.staticContext
		ext.mu.Unlock()
		if strings.TrimSpace(txt) == "" {
			continue
		}
		blocks = append(blocks, wrapContext(ext.Manifest.Name, "", clampBytes(txt, maxStaticContextBytes)))
	}
	return strings.Join(blocks, "\n\n")
}

// EphemeralContext returns the per-turn model-context block: every
// active card, host-wrapped and attributed, ordered by (priority, then
// extension name, then card id), truncated to the total byte budget.
// Pulled once per turn by the agent and injected at the cache-free tail;
// never persisted. Empty when no cards are set.
func (d *Driver) EphemeralContext() string {
	type entry struct {
		source   string
		id       string
		card     contextCard
		priority int
	}
	exts := d.snapshotExts()
	disabled := d.contextDisabledSet()
	var entries []entry
	for _, ext := range exts {
		if disabled[ext.Manifest.Name] {
			continue
		}
		ext.mu.Lock()
		for id, c := range ext.contextCards {
			entries = append(entries, entry{source: ext.Manifest.Name, id: id, card: c, priority: c.priority})
		}
		ext.mu.Unlock()
	}
	if len(entries) == 0 {
		return ""
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].priority != entries[j].priority {
			return entries[i].priority < entries[j].priority
		}
		if entries[i].source != entries[j].source {
			return entries[i].source < entries[j].source
		}
		return entries[i].id < entries[j].id
	})

	var blocks []string
	// The guard is charged to the budget up front rather than added free at the
	// end, so maxEphemeralBytes keeps meaning what it says.
	//
	// A whitespace-only guard means NO guard. The eval's control arm has to
	// serve the exact pre-guard text, and the i18n catalog cannot express an
	// empty override: keyedText treats "" as a miss and falls back to the
	// compiled English, so an empty overlay would silently serve the very
	// sentence it was written to remove -- an arm that scores the shipped text
	// twice while reporting a comparison. See
	// scripts/eval/overlays/tail-background-guard-off.json.
	guard := strings.TrimSpace(tailBackgroundGuard())
	// Charged up front for the same reason as the leading guard: a trailing
	// guard is still bytes this section spends, and maxEphemeralBytes has to
	// keep meaning what it says under either arm. Absent by default.
	trailing := strings.TrimSpace(tailBackgroundGuardTrailing())
	total := 0
	if guard != "" {
		total += len(guard) + 2
	}
	if trailing != "" {
		total += len(trailing) + 2
	}
	for _, e := range entries {
		block := wrapContext(e.source, e.card.label, clampBytes(e.card.text, maxCardBytes))
		// +2 for the join separator between blocks.
		if total+len(block)+2 > maxEphemeralBytes && len(blocks) > 0 {
			blocks = append(blocks, "<extension-context note=\"truncated\">further extension context omitted (budget)</extension-context>")
			break
		}
		blocks = append(blocks, block)
		total += len(block) + 2
	}
	// Empty stays empty: callers test this for "" to decide whether the section
	// exists at all, and a lone guard introducing nothing would be worse than
	// silence.
	if len(blocks) == 0 {
		return ""
	}
	out := strings.Join(blocks, "\n\n")
	if guard != "" {
		out = guard + "\n\n" + out
	}
	if trailing != "" {
		out = out + "\n\n" + trailing
	}
	return out
}

// tailBackgroundGuardTrailing renders the same prohibition as
// tailBackgroundGuard, after the section instead of before it. Absent by
// default -- the compiled fallback is a single space, which TrimSpace reduces
// to "" -- so the shipped section is byte-identical to the one that predates
// this key.
//
// It renders the twin of the key in packages/agent/build for the same reason
// the leading guard does: one catalog entry, so the two call sites cannot
// drift into two almost-identical prohibitions. The argument for why position
// needed to become expressible at all lives on the twin.
func tailBackgroundGuardTrailing() string {
	return i18n.P("tail.background.guard.trailing", " ")
}

// tailBackgroundGuard leads the extension-context section on the ephemeral
// tail. It renders the SAME i18n key as the lore frame's guard in
// packages/agent/build, deliberately: both are reference blocks riding the same
// tail, and one catalog entry keeps them from drifting into two prohibitions
// that say almost-but-not-quite the same thing.
//
// Emitted once for the section, not once per card. The wrapper already attributes
// each card to its extension; repeating a prohibition per card would spend the
// budget on saying the same sentence to the model five times.
//
// The BACKGROUND shape is the point: a card is live guidance an extension wrote
// to be FOLLOWED -- the installed obsidian card carries a destructive-write
// safety rule ("echo the path back exactly, never guess") -- so it must not be
// told to proceed as if the card were absent, and must not be told the card is
// not to be acted on. Only the reply-hijack is prohibited.
//
// The guard therefore carries no act-prohibition. It briefly did; that clause
// was removed after it measured as worthless on lore and read as actively
// harmful here. See the note on the twin in packages/agent/build.
//
// What was NOT measured, here or on the twin, is the guard's position within
// the block. EphemeralContext prepends it unconditionally, so no overlay can
// produce a guard-last section, and the eval's every-rung-at-ceiling result
// covers presence only. The hijack this guard exists to prevent was a
// guard-last failure. The twin's comment carries the full argument.
//
// The built-in task board is NOT one of these cards, despite an earlier version
// of this comment calling it the standing example. It rides as its own provider
// through EphemeralTail.Tasks and never passes through this section.
func tailBackgroundGuard() string {
	return i18n.P("tail.background.guard",
		"[background] Do not reply to this block and do not mention it in your answer. It is background you may draw on.")
}

// HasBlockingContext reports whether any context-enabled extension has
// a card marked blocking — open work the model should review before
// declaring done. The host's at-close gate uses it to decide whether to
// re-prompt the model once when it tries to finish.
func (d *Driver) HasBlockingContext() bool {
	exts := d.snapshotExts()
	disabled := d.contextDisabledSet()
	for _, ext := range exts {
		if disabled[ext.Manifest.Name] {
			continue
		}
		ext.mu.Lock()
		for _, c := range ext.contextCards {
			if c.blocking {
				ext.mu.Unlock()
				return true
			}
		}
		ext.mu.Unlock()
	}
	return false
}

// StatusSegments returns every status-bar segment, source-prefixed and
// ordered by extension name then id, for the TUI to render. Not
// model-facing.
func (d *Driver) StatusSegments() []string {
	exts := d.snapshotExts()
	var out []string
	for _, ext := range exts {
		ext.mu.Lock()
		ids := make([]string, 0, len(ext.statusSegments))
		for id := range ext.statusSegments {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			out = append(out, ext.statusSegments[id])
		}
		ext.mu.Unlock()
	}
	return out
}

// ContextItem is one entry in the inspector's view of what extensions
// contribute to the model — a static contribution or a live card.
type ContextItem struct {
	Source string
	Kind   string // "static" or "card"
	ID     string // card id; empty for static
	Label  string
	Text   string
}

// ContextSnapshot returns a flat, ordered view of every static
// contribution and active card across all extensions, for the /context
// inspector so the user can see exactly what is being injected into the
// model. Static items first (by source), then cards by (priority,
// source, id).
func (d *Driver) ContextSnapshot() []ContextItem {
	exts := d.snapshotExts()
	disabled := d.contextDisabledSet()
	var statics []ContextItem
	type cardEntry struct {
		item     ContextItem
		priority int
	}
	var cards []cardEntry
	for _, ext := range exts {
		if disabled[ext.Manifest.Name] {
			continue
		}
		ext.mu.Lock()
		if strings.TrimSpace(ext.staticContext) != "" {
			statics = append(statics, ContextItem{Source: ext.Manifest.Name, Kind: "static", Text: ext.staticContext})
		}
		for id, c := range ext.contextCards {
			cards = append(cards, cardEntry{
				item:     ContextItem{Source: ext.Manifest.Name, Kind: "card", ID: id, Label: c.label, Text: c.text},
				priority: c.priority,
			})
		}
		ext.mu.Unlock()
	}
	sort.Slice(cards, func(i, j int) bool {
		if cards[i].priority != cards[j].priority {
			return cards[i].priority < cards[j].priority
		}
		if cards[i].item.Source != cards[j].item.Source {
			return cards[i].item.Source < cards[j].item.Source
		}
		return cards[i].item.ID < cards[j].item.ID
	})
	out := statics
	for _, c := range cards {
		out = append(out, c.item)
	}
	return out
}

// snapshotExts returns the current extensions sorted by name, so every
// aggregation is deterministic regardless of map iteration order.
func (d *Driver) snapshotExts() []*Extension {
	d.mu.RLock()
	exts := make([]*Extension, 0, len(d.ext))
	for _, e := range d.ext {
		exts = append(exts, e)
	}
	d.mu.RUnlock()
	sort.Slice(exts, func(i, j int) bool { return exts[i].Manifest.Name < exts[j].Manifest.Name })
	return exts
}

// wrapContext frames extension-supplied text as a clearly-attributed,
// non-authoritative block. The host owns the wrapper so an extension
// can never speak as system or as the user; escapeContext keeps the
// payload from breaking out of the frame or forging a higher-authority
// block.
func wrapContext(source, label, text string) string {
	attrs := fmt.Sprintf("source=%q", source)
	if label != "" {
		attrs += fmt.Sprintf(" label=%q", label)
	}
	return "<extension-context " + attrs + ">\n" + escapeContext(text) + "\n</extension-context>"
}

// escapeContext neutralizes the few tag-like sequences an extension
// could use to break its wrapper or impersonate a host/system block.
// Targeted (not a blanket angle-bracket escape) so ordinary `<`/`>` in
// task text or code survives intact.
var contextEscaper = strings.NewReplacer(
	"</extension-context", "<\\/extension-context",
	"<extension-context", "<\\extension-context",
	"<system-reminder", "<\\system-reminder",
	"</system-reminder", "<\\/system-reminder",
)

func escapeContext(s string) string { return contextEscaper.Replace(s) }

// clampBytes truncates s to at most max bytes on a valid UTF-8
// boundary, appending a marker when it cut anything.
func clampBytes(s string, max int) string {
	if len(s) <= max {
		return s
	}
	t := s[:max]
	for len(t) > 0 && !utf8.ValidString(t) {
		t = t[:len(t)-1]
	}
	return t + "\n…[truncated]"
}
