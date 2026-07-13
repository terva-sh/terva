package core

// Title-seed assembly for session auto/on-demand titling
// (docs/proposals/resume-picker.md stage 3). One pure builder serves the
// automatic settleTitle pass, the sessions.generate_title verb, and any
// future re-title-on-compaction hook: at first-exchange time the cascade
// degenerates to the first user message — exactly the pre-cascade behavior —
// and on a long, drifted session it anchors on the latest compaction summary
// with the most recent exchanges packed alongside.

import (
	"strings"
	"unicode"

	"terva.sh/terva/packages/provider"
)

// TitleSeedBudget is the default seed size in characters (~2k tokens at the
// 1 token ≈ 4 chars heuristic — exactness buys nothing at this scale, and
// the title model replies with at most a few words).
const TitleSeedBudget = 8000

// titleSeedMsgClamp caps each recent message's excerpt. Breadth beats depth
// for titling: five clipped exchanges name a topic better than one full
// message, and without the clamp a single giant paste eats the whole
// recent-messages section.
const titleSeedMsgClamp = 450

// titleSeedRecentFloor is the fraction (percent) of the budget reserved for
// recent messages whenever any exist. Compactions can be long; without the
// floor a big one starves the recency section on exactly the sessions whose
// current focus has drifted furthest from the anchor.
const titleSeedRecentFloor = 35

// clipMarker is the elision marker ClipMiddle inserts. It counts against
// the caller's budget so packing math stays honest.
const clipMarker = " [… truncated …] "

// compactionSummaryHeader is the prefix compact.go stamps on the synthetic
// summary message. BuildTitleSeed strips it — the seed labels its own
// sections.
const compactionSummaryHeader = "## Context Summary (compacted)"

// ClipMiddle truncates s to at most budget characters (runes) by cutting
// from the middle, keeping the head and tail around an elision marker:
// openings carry intent, endings carry the concrete ask (messages) or the
// current state (compaction summaries). Cuts snap to rune boundaries by
// construction and prefer whitespace so no word fragments waste the seed.
// The marker counts against the budget. A budget too small for the marker
// degrades to a plain head cut.
func ClipMiddle(s string, budget int) string {
	if budget <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= budget {
		return s
	}
	marker := []rune(clipMarker)
	if budget <= len(marker)+2 {
		// No room for head + marker + tail; a bare head cut keeps the most
		// information per character at these sizes.
		return string(runes[:budget])
	}
	keep := budget - len(marker)
	head := (keep + 1) / 2
	tail := keep - head
	headEnd := snapBack(runes, head)
	tailStart := snapForward(runes, len(runes)-tail)
	return string(runes[:headEnd]) + clipMarker + string(runes[tailStart:])
}

// snapBack moves a cut position leftwards onto the nearest whitespace within
// a small window, so the head doesn't end mid-word. Returns the original
// position when no whitespace is near — never widens the cut.
func snapBack(runes []rune, pos int) int {
	const window = 16
	for i := pos; i > pos-window && i > 0; i-- {
		if unicode.IsSpace(runes[i-1]) {
			return i
		}
	}
	return pos
}

// snapForward moves a cut position rightwards onto the character after the
// nearest whitespace within a small window, so the tail doesn't start
// mid-word.
func snapForward(runes []rune, pos int) int {
	const window = 16
	for i := pos; i < pos+window && i < len(runes); i++ {
		if unicode.IsSpace(runes[i]) {
			return i + 1
		}
	}
	return pos
}

// messageText concatenates a message's text blocks. Tool calls, tool
// results, and reasoning blocks are skipped everywhere in the seed: they are
// high-volume and near-zero-signal for a title.
func messageText(m provider.Message) string {
	var b strings.Builder
	for _, c := range m.Content {
		if tb, ok := c.(provider.TextBlock); ok && tb.Text != "" {
			if b.Len() > 0 {
				b.WriteString("\n")
			}
			b.WriteString(tb.Text)
		}
	}
	return strings.TrimSpace(b.String())
}

// isCompactionMessage reports whether m is the synthetic summary message a
// compaction leaves in the transcript (compact.go stamps it).
func isCompactionMessage(m provider.Message) bool {
	return m.Meta != nil && m.Meta["compaction"] == "true"
}

// BuildTitleSeed assembles the model seed for titling a session from its
// transcript. Budget is in characters (see TitleSeedBudget); section labels
// and separators ride on top of it — the budget governs content, not
// framing, and at these sizes approximate is fine.
//
// Shape: an anchor section — the LATEST compaction summary when one exists
// (a good compaction front-loads the original goal and back-loads "where we
// left off"), else the first user message — followed by the most recent
// exchanges packed newest-first at whole-message granularity but emitted
// chronologically. When recent messages exist they are guaranteed
// titleSeedRecentFloor percent of the budget and the anchor is truncated
// (middle-out) to make room. Returns "" when the transcript has no
// user/assistant text at all.
func BuildTitleSeed(msgs []provider.Message, budget int) string {
	if budget <= 0 {
		return ""
	}

	// Anchor: latest compaction, else first user message with text.
	anchorIdx := -1
	anchorIsCompaction := false
	for i := len(msgs) - 1; i >= 0; i-- {
		if isCompactionMessage(msgs[i]) && messageText(msgs[i]) != "" {
			anchorIdx = i
			anchorIsCompaction = true
			break
		}
	}
	if anchorIdx == -1 {
		for i, m := range msgs {
			if m.Role == provider.RoleUser && messageText(m) != "" {
				anchorIdx = i
				break
			}
		}
	}
	if anchorIdx == -1 {
		return ""
	}
	anchorText := messageText(msgs[anchorIdx])
	if anchorIsCompaction {
		anchorText = strings.TrimSpace(strings.TrimPrefix(anchorText, compactionSummaryHeader))
	}

	// Recent exchanges: user/assistant text after the anchor, per-message
	// clamped. Later compaction messages can't appear (the anchor IS the
	// latest one), but skip any defensively.
	type excerpt struct {
		role string
		text string
	}
	var recents []excerpt
	for i := anchorIdx + 1; i < len(msgs); i++ {
		m := msgs[i]
		if isCompactionMessage(m) {
			continue
		}
		if m.Role != provider.RoleUser && m.Role != provider.RoleAssistant {
			continue
		}
		text := messageText(m)
		if text == "" {
			continue
		}
		recents = append(recents, excerpt{role: string(m.Role), text: ClipMiddle(text, titleSeedMsgClamp)})
	}

	// Budget split: the anchor takes what the recency floor leaves; unused
	// anchor budget flows back to the recents.
	anchorMax := budget
	if len(recents) > 0 {
		anchorMax = budget - budget*titleSeedRecentFloor/100
	}
	anchorText = ClipMiddle(anchorText, anchorMax)
	remaining := budget - len([]rune(anchorText))

	// Pack newest-first at whole-excerpt granularity; when not even the
	// newest fits, clip it into whatever room is left rather than emitting
	// an empty recency section (the floor promised one).
	var picked []excerpt
	for i := len(recents) - 1; i >= 0; i-- {
		line := recents[i]
		cost := len([]rune(line.role)) + 2 + len([]rune(line.text)) + 1
		if cost > remaining {
			if len(picked) == 0 && remaining > 80 {
				line.text = ClipMiddle(line.text, remaining-len([]rune(line.role))-3)
				picked = append(picked, line)
				remaining = 0
			}
			break
		}
		picked = append(picked, line)
		remaining -= cost
	}

	var b strings.Builder
	if anchorIsCompaction {
		b.WriteString("Summary of the session so far:\n")
	} else {
		b.WriteString("The conversation opens with:\n")
	}
	b.WriteString(anchorText)
	if len(picked) > 0 {
		b.WriteString("\n\nMost recent exchanges:\n")
		// picked is newest-first; emit chronologically.
		for i := len(picked) - 1; i >= 0; i-- {
			b.WriteString(picked[i].role)
			b.WriteString(": ")
			b.WriteString(picked[i].text)
			b.WriteString("\n")
		}
	}
	return strings.TrimSpace(b.String())
}
