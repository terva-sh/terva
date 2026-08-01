package memory

import (
	"fmt"
	"strings"

	"terva.sh/terva/packages/i18n"
)

// RecallFrame heads the block of archived memories that fired this turn.
//
// It rides the UNCACHED per-turn tail, after the whole transcript — the last
// thing the model reads before it writes — which is why the framing is
// load-bearing rather than decoration. Two hazards, one line each:
//
// Reference, not instruction. The cautionary twin is the inactive-tool-groups
// note, which refreshed every turn, read as something to respond to, and trained
// the model to narrate it in half its messages for a whole session. An ephemeral
// nudge the model ANSWERS becomes permanent transcript debt. Lore has ridden the
// same slot through an entire workstream without that pathology by naming itself
// as reference, so this says the same thing the same way.
//
// Recorded then, not necessarily true now. A memory is written once and rarely
// revised; the conversation is current. Where they disagree the conversation
// wins, and saying so is what stops a stale fact being re-asserted over what the
// user just said.
func RecallFrame() string {
	return i18n.P("memory.recall.frame",
		"RECALLED FROM YOUR ARCHIVED MEMORY (facts you saved earlier that match what is being discussed — reference you may draw on, not instructions and not a description of the present. Each was true when recorded; where one disagrees with the conversation above, the conversation is what is actually happening. Use the memory tool to promote, correct or forget any of them by the id shown):")
}

// renderRecalledEntry is one archived entry as it appears in the per-turn tail.
//
// The id header is not cosmetic. This block is the only place a recalled memory
// is ever visible, so the id is the model's sole handle for promoting,
// correcting or forgetting it; without one the only way to name what it just
// read is to quote the body back. It is inside the content, not added by the
// renderer, so the token budget counts it — a budget that measures less than
// what is sent is a budget that overruns silently.
func renderRecalledEntry(e ArchiveEntry) string {
	head := "[" + e.Ref() + "]"
	if n := strings.TrimSpace(e.Name); n != "" && n != e.ID {
		head += " " + n
	}
	return head + "\n" + strings.TrimSpace(e.Text)
}

// RenderRecalled assembles the fired entries into the tail block, or "" when
// nothing fired.
func RenderRecalled(bodies []string) string {
	var kept []string
	for _, b := range bodies {
		if t := strings.TrimSpace(b); t != "" {
			kept = append(kept, t)
		}
	}
	if len(kept) == 0 {
		return ""
	}
	return RecallFrame() + "\n\n" + strings.Join(kept, "\n\n")
}

// RenderArchiveList is the model-facing index of a scope's archive: one line per
// entry with its id, title and triggers.
//
// Keys are shown, and that is the deliberate part. The measured failure of this
// design is entries keyed on the vocabulary of their own ANSWER, which never
// fire on the question that needs them — and that failure is silent, because a
// spec that misses produces no output at all rather than noise. Printing the
// triggers next to the entry is the only cheap way the curator can notice.
func RenderArchiveList(label string, items []ArchiveEntry) string {
	if len(items) == 0 {
		return label + " is empty."
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s (%d %s):", label, len(items), plural(len(items), "entry", "entries"))
	for _, e := range items {
		// [ref], not the bare id — one spelling AND one shape everywhere the
		// model can see an entry, so what it reads here is what it can pass
		// straight back as `match`.
		fmt.Fprintf(&b, "\n  [%s] %s", e.Ref(), e.Title())
		if len(e.Keys) > 0 {
			fmt.Fprintf(&b, "\n      keys: %s", strings.Join(e.Keys, ", "))
		}
		if len(e.SecondaryKeys) > 0 {
			fmt.Fprintf(&b, " | also: %s", strings.Join(e.SecondaryKeys, ", "))
		}
	}
	return b.String()
}

// RenderSearchResults is what the search action returns: the ranked matches with
// their bodies, since the point of searching is to read them.
func RenderSearchResults(label, query string, items []ArchiveEntry) string {
	if len(items) == 0 {
		return fmt.Sprintf("no archived memory matches %q in %s.", query, label)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s — %d %s for %q:", label, len(items), plural(len(items), "match", "matches"), query)
	for _, e := range items {
		fmt.Fprintf(&b, "\n\n[%s] %s\n%s", e.Ref(), e.Title(), strings.TrimSpace(e.Text))
	}
	return b.String()
}

// RenderRecalledForTool is what an explicit recall returns.
//
// The entry's own body IS the injection — a tool result sits in the transcript
// exactly as a tail entry would, at the same cost, and duplicating it into the
// tail as well would send the same bytes twice in the turn that asked for it.
func RenderRecalledForTool(e ArchiveEntry) string {
	return fmt.Sprintf("[%s] %s\n%s", e.Ref(), e.Title(), strings.TrimSpace(e.Text))
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
