package dialogs

import (
	"regexp"
	"strings"

	"terva.sh/terva/packages/tui"
)

// Copy parts: the domain layer beneath the copy picker.
//
// tui.SplitBlocks decides where a markdown message divides. This file
// dresses those blocks for a picker: it stamps the role and the message
// they came from, extracts the verbatim text that will reach the
// clipboard, and builds a one-line preview for the list row.
//
// Text is a slice of the original markdown, so a copy carries the
// author's source: no wrap, no gutter, no ANSI. Preview is cosmetic and
// is never copied.

// PartRole says which side of the conversation a part came from. Tool
// results are not a role: they are never offered for copying.
type PartRole int

const (
	RoleUser PartRole = iota
	RoleThinking
	RoleReply
)

// String returns a stable identifier, not a label for display. The
// picker translates its own visible labels, so these do not enter the
// i18n catalogs.
func (r PartRole) String() string {
	switch r {
	case RoleUser:
		return "user"
	case RoleThinking:
		return "thinking"
	default:
		return "reply"
	}
}

// Part is one addressable piece of a message: what to copy, what to
// show, and where it came from.
type Part struct {
	Role    PartRole
	MsgIdx  int // the message within the turn, so the picker can group
	Kind    tui.BlockKind
	Text    string // verbatim markdown -- this is what gets copied
	Preview string // one line, never truncated; the picker fits it
}

// SplitParts turns one message's markdown into copyable parts. A blank
// message yields none.
func SplitParts(role PartRole, msgIdx int, md string) []Part {
	blocks := tui.SplitBlocks(md)
	if len(blocks) == 0 {
		return nil
	}
	parts := make([]Part, 0, len(blocks))
	for _, b := range blocks {
		text := md[b.Start:b.End]
		parts = append(parts, Part{
			Role:    role,
			MsgIdx:  msgIdx,
			Kind:    b.Kind,
			Text:    text,
			Preview: previewOf(b.Kind, text),
		})
	}
	return parts
}

// previewOf builds the single line a picker row shows. It strips the
// markup that identifies nothing -- the fence marker, the bullet, the
// hashes -- so the row carries content instead of syntax.
//
// These strippers are cosmetic. Unlike the block boundaries, which come
// from the renderer's own grammar, a mismatch here shows a stray "- " in
// a row and never affects what is copied.
func previewOf(kind tui.BlockKind, text string) string {
	switch kind {
	case tui.BlockFence:
		return fencePreview(text)
	case tui.BlockHeading:
		// The heading only. A heading block absorbs the prose beneath
		// it, but the heading is what a reader scans for, and the prose
		// still travels in Text.
		return collapseWS(headingMarkRE.ReplaceAllString(firstLine(text), ""))
	case tui.BlockList:
		// The first item only. Concatenating every item reads as noise.
		return collapseWS(listMarkerRE.ReplaceAllString(firstLine(text), ""))
	case tui.BlockQuote:
		return collapseWS(quoteMarkRE.ReplaceAllString(text, ""))
	case tui.BlockTable:
		return collapseWS(firstLine(text))
	default:
		return collapseWS(text)
	}
}

// fencePreview shows the first line of actual code. "```go" identifies
// nothing, so the markers are dropped; an empty fence falls back to its
// language rather than leaving a blank row.
func fencePreview(text string) string {
	lines := strings.Split(text, "\n")
	lang := strings.TrimSpace(strings.TrimPrefix(strings.TrimLeft(firstLine(text), " "), "```"))
	for i, l := range lines {
		if i == 0 || isFenceLine(l) {
			continue
		}
		if strings.TrimSpace(l) != "" {
			return collapseWS(l)
		}
	}
	if lang != "" {
		return lang
	}
	return "code"
}

func isFenceLine(s string) bool { return strings.HasPrefix(strings.TrimLeft(s, " "), "```") }

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// collapseWS folds every run of whitespace, newlines included, into a
// single space. A picker row is one line, and a preview that carried a
// newline would break the list layout.
func collapseWS(s string) string { return strings.Join(strings.Fields(s), " ") }

var (
	headingMarkRE = regexp.MustCompile(`^\s*#{1,6}\s+`)
	listMarkerRE  = regexp.MustCompile(`^\s*(?:[-*+]|\d+\.)\s+`)
	quoteMarkRE   = regexp.MustCompile(`(?m)^\s*>\s?`)
)
