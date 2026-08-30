package tui

import "strings"

// Markdown block segmentation for copy-out.
//
// SplitBlocks answers "what are the addressable pieces of this message?"
// so a picker can offer a paragraph, a code fence, or a table on its own
// instead of the whole reply. It deliberately lives beside RenderMarkdown
// and reuses that renderer's grammar -- headingRE, bulletRE, numberRE,
// looksLikeTableHeader and the ``` fence toggle. A second, independent
// parser would drift from the renderer, and a drifted split mislabels
// what the user is about to copy.
//
// A Block is a pair of byte offsets into the ORIGINAL source, never a
// rebuilt string. Copying is therefore src[Start:End]: the author's own
// markdown, with no wrap, no gutter and no ANSI, and it stays correct if
// the terminal is resized between choosing and copying.

// BlockKind labels a block so a picker can show a sigil and filter by
// kind ("show me only the code").
type BlockKind int

const (
	BlockParagraph BlockKind = iota
	BlockHeading
	BlockList
	BlockFence
	BlockTable
	BlockQuote
)

func (k BlockKind) String() string {
	switch k {
	case BlockHeading:
		return "heading"
	case BlockList:
		return "list"
	case BlockFence:
		return "fence"
	case BlockTable:
		return "table"
	case BlockQuote:
		return "quote"
	default:
		return "paragraph"
	}
}

// Block is one addressable region of a markdown source. Start is
// inclusive and End exclusive, so src[Start:End] is the exact text.
type Block struct {
	Kind  BlockKind
	Start int
	End   int
}

// SplitBlocks divides src into copyable blocks. Blank runs between
// blocks belong to no block, so a copied region never carries stray
// leading or trailing newlines. An empty or all-blank source yields no
// blocks at all.
func SplitBlocks(src string) []Block {
	lines := strings.Split(src, "\n")
	starts := make([]int, len(lines))
	off := 0
	for i, l := range lines {
		starts[i] = off
		off += len(l) + 1 // +1 for the \n that Split consumed
	}
	lineEnd := func(i int) int { return starts[i] + len(lines[i]) }

	var blocks []Block
	for i := 0; i < len(lines); {
		if isBlank(lines[i]) {
			i++
			continue
		}
		b, next := parseBlock(lines, starts, lineEnd, i)
		if b.End > b.Start {
			blocks = append(blocks, b)
		}
		if next <= i { // defensive: never spin on a line
			next = i + 1
		}
		i = next
	}
	return blocks
}

// parseBlock reads exactly one block beginning at line i, which the
// caller guarantees is non-blank. It returns the block and the index of
// the first line after it.
func parseBlock(lines []string, starts []int, lineEnd func(int) int, i int) (Block, int) {
	start := starts[i]

	if isFenceMarker(lines[i]) {
		// The closing marker is part of the block. An unterminated
		// fence -- normal mid-stream, and tolerated by RenderMarkdown
		// -- runs to the last non-blank line instead of to EOF, so a
		// trailing newline does not ride along.
		for j := i + 1; j < len(lines); j++ {
			if isFenceMarker(lines[j]) {
				return Block{Kind: BlockFence, Start: start, End: lineEnd(j)}, j + 1
			}
		}
		last := lastNonBlank(lines, i)
		return Block{Kind: BlockFence, Start: start, End: lineEnd(last)}, len(lines)
	}

	if i+1 < len(lines) && looksLikeTableHeader(lines[i], lines[i+1]) {
		j := i + 2
		for j < len(lines) && looksLikeTableRow(lines[j]) {
			j++
		}
		return Block{Kind: BlockTable, Start: start, End: lineEnd(j - 1)}, j
	}

	if headingRE.MatchString(lines[i]) {
		end := lineEnd(i)
		next := i + 1
		// A bare "## Segmentation" on the clipboard is useless, so a
		// heading takes the prose beneath it. Only prose: a fence or a
		// table under a heading is usually the thing being copied, and
		// it stays addressable on its own.
		if k := nextNonBlank(lines, next); k >= 0 {
			if sub, after := parseBlock(lines, starts, lineEnd, k); isProse(sub.Kind) {
				end, next = sub.End, after
			}
		}
		return Block{Kind: BlockHeading, Start: start, End: end}, next
	}

	if isQuote(lines[i]) {
		j := i
		for j+1 < len(lines) && isQuote(lines[j+1]) {
			j++
		}
		return Block{Kind: BlockQuote, Start: start, End: lineEnd(j)}, j + 1
	}

	if isListItem(lines[i]) {
		end, next := scanList(lines, lineEnd, i)
		return Block{Kind: BlockList, Start: start, End: end}, next
	}

	// Paragraph: runs to a blank line, or to wherever another
	// construct begins, because the renderer switches modes there too.
	j := i
	for j+1 < len(lines) && !isBlank(lines[j+1]) && !startsBlock(lines, j+1) {
		j++
	}
	return Block{Kind: BlockParagraph, Start: start, End: lineEnd(j)}, j + 1
}

// scanList consumes a list, including a loose one whose items are
// separated by blank lines, and any indented continuation lines. Per-item
// blocks would be noise at paragraph granularity, so the list is one
// part. The end lands on the last content line, never on a blank.
func scanList(lines []string, lineEnd func(int) int, i int) (end, next int) {
	last := i
	j := i + 1
	for j < len(lines) {
		switch {
		case isListItem(lines[j]), isContinuation(lines[j]):
			last, j = j, j+1
		case isBlank(lines[j]):
			// A blank keeps the list alive only if another item
			// follows it. Otherwise the list ended at the last item.
			k := nextNonBlank(lines, j)
			if k < 0 || !isListItem(lines[k]) {
				return lineEnd(last), j
			}
			j = k
		default:
			return lineEnd(last), j
		}
	}
	return lineEnd(last), j
}

// startsBlock reports whether line i opens a construct other than a
// paragraph, which means the paragraph before it has to end.
func startsBlock(lines []string, i int) bool {
	if isFenceMarker(lines[i]) || headingRE.MatchString(lines[i]) || isQuote(lines[i]) || isListItem(lines[i]) {
		return true
	}
	return i+1 < len(lines) && looksLikeTableHeader(lines[i], lines[i+1])
}

func isProse(k BlockKind) bool {
	return k == BlockParagraph || k == BlockList || k == BlockQuote
}

func isBlank(s string) bool { return strings.TrimSpace(s) == "" }

func isFenceMarker(s string) bool { return strings.HasPrefix(strings.TrimLeft(s, " "), "```") }

func isQuote(s string) bool { return strings.HasPrefix(strings.TrimLeft(s, " "), ">") }

func isListItem(s string) bool { return bulletRE.MatchString(s) || numberRE.MatchString(s) }

// isContinuation reports an indented, non-blank line: the wrapped body of
// the list item above it.
func isContinuation(s string) bool {
	return !isBlank(s) && (strings.HasPrefix(s, " ") || strings.HasPrefix(s, "\t"))
}

func nextNonBlank(lines []string, from int) int {
	for i := from; i < len(lines); i++ {
		if !isBlank(lines[i]) {
			return i
		}
	}
	return -1
}

func lastNonBlank(lines []string, from int) int {
	last := from
	for i := from; i < len(lines); i++ {
		if !isBlank(lines[i]) {
			last = i
		}
	}
	return last
}
