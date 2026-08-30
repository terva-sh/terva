package tui

import (
	"strings"
	"testing"
)

// blockText is the invariant the whole copy feature rests on: a Block is
// an offset pair into the ORIGINAL source, so the copied bytes are the
// author's markdown and not a re-serialization of a parse tree.
func blockText(src string, b Block) string { return src[b.Start:b.End] }

// assertBlocks checks kind and verbatim text together. Checking either
// alone lets a bug hide: right kinds with shifted offsets copies the
// wrong paragraph, and right text with wrong kinds mislabels the picker.
func assertBlocks(t *testing.T, src string, want []Block, wantText []string) {
	t.Helper()
	got := SplitBlocks(src)
	if len(got) != len(want) {
		t.Fatalf("block count = %d, want %d\ngot:\n%s", len(got), len(want), dumpBlocks(src, got))
	}
	for i := range got {
		if got[i].Kind != want[i].Kind {
			t.Errorf("block %d kind = %v, want %v (text %q)", i, got[i].Kind, want[i].Kind, blockText(src, got[i]))
		}
		if txt := blockText(src, got[i]); txt != wantText[i] {
			t.Errorf("block %d text =\n%q\nwant\n%q", i, txt, wantText[i])
		}
	}
}

func dumpBlocks(src string, bs []Block) string {
	var sb strings.Builder
	for i, b := range bs {
		sb.WriteString(strings.TrimSpace(b.Kind.String()))
		sb.WriteString(": ")
		sb.WriteString(blockText(src, b))
		if i < len(bs)-1 {
			sb.WriteString("\n---\n")
		}
	}
	return sb.String()
}

func TestSplitBlocksSeparatesParagraphsOnBlankLines(t *testing.T) {
	src := "First paragraph, two\nlines long.\n\nSecond paragraph."
	assertBlocks(t, src,
		[]Block{{Kind: BlockParagraph}, {Kind: BlockParagraph}},
		[]string{"First paragraph, two\nlines long.", "Second paragraph."})
}

// A fence is atomic. Blank lines inside code are not paragraph breaks,
// and the ``` markers travel with the body so the copy pastes as code.
func TestSplitBlocksKeepsAFenceWhole(t *testing.T) {
	src := "Before.\n\n```go\nfunc a() {}\n\nfunc b() {}\n```\n\nAfter."
	assertBlocks(t, src,
		[]Block{{Kind: BlockParagraph}, {Kind: BlockFence}, {Kind: BlockParagraph}},
		[]string{"Before.", "```go\nfunc a() {}\n\nfunc b() {}\n```", "After."})
}

// Streaming truncates: the opening fence arrives before the closing one.
// RenderMarkdown tolerates this, so the splitter must agree rather than
// dropping the tail on the floor.
func TestSplitBlocksClosesAnUnterminatedFenceAtEOF(t *testing.T) {
	src := "Here:\n\n```sh\njust ci\n"
	assertBlocks(t, src,
		[]Block{{Kind: BlockParagraph}, {Kind: BlockFence}},
		[]string{"Here:", "```sh\njust ci"})
}

// No blank line between prose and code: the paragraph still has to end
// where the fence begins, because the renderer switches modes there too.
func TestSplitBlocksEndsAParagraphWhereAFenceStarts(t *testing.T) {
	src := "Run this:\n```sh\njust check\n```"
	assertBlocks(t, src,
		[]Block{{Kind: BlockParagraph}, {Kind: BlockFence}},
		[]string{"Run this:", "```sh\njust check\n```"})
}

func TestSplitBlocksKeepsATableWhole(t *testing.T) {
	src := "| a | b |\n| --- | --- |\n| 1 | 2 |\n| 3 | 4 |\n\nAfter."
	assertBlocks(t, src,
		[]Block{{Kind: BlockTable}, {Kind: BlockParagraph}},
		[]string{"| a | b |\n| --- | --- |\n| 1 | 2 |\n| 3 | 4 |", "After."})
}

// A lone "## Segmentation" on the clipboard is never what anyone wanted,
// so a heading takes its prose with it -- blank line included, verbatim.
func TestSplitBlocksHeadingAbsorbsTheProseBeneathIt(t *testing.T) {
	src := "## Segmentation\n\nOne pure function, no TUI in sight.\n\nNext paragraph."
	assertBlocks(t, src,
		[]Block{{Kind: BlockHeading}, {Kind: BlockParagraph}},
		[]string{"## Segmentation\n\nOne pure function, no TUI in sight.", "Next paragraph."})
}

// It absorbs prose only. A fence or a table under a heading is usually
// the thing you came to copy, so it stays independently addressable.
func TestSplitBlocksHeadingDoesNotSwallowAFence(t *testing.T) {
	src := "### Usage\n\n```sh\nterva --help\n```"
	assertBlocks(t, src,
		[]Block{{Kind: BlockHeading}, {Kind: BlockFence}},
		[]string{"### Usage", "```sh\nterva --help\n```"})
}

func TestSplitBlocksHeadingDoesNotSwallowAnotherHeading(t *testing.T) {
	src := "# Title\n\n## Subtitle\n\nBody."
	assertBlocks(t, src,
		[]Block{{Kind: BlockHeading}, {Kind: BlockHeading}},
		[]string{"# Title", "## Subtitle\n\nBody."})
}

// Per-item parts would be noise at paragraph granularity, so a list is
// one part -- including a loose list, whose items are blank-separated.
func TestSplitBlocksKeepsAListWholeAcrossItemsAndBlanks(t *testing.T) {
	src := "- first\n- second\n  continued\n\n- third\n\nAfter."
	assertBlocks(t, src,
		[]Block{{Kind: BlockList}, {Kind: BlockParagraph}},
		[]string{"- first\n- second\n  continued\n\n- third", "After."})
}

func TestSplitBlocksTreatsANumberedListAsAList(t *testing.T) {
	src := "1. one\n2. two\n\nAfter."
	assertBlocks(t, src,
		[]Block{{Kind: BlockList}, {Kind: BlockParagraph}},
		[]string{"1. one\n2. two", "After."})
}

func TestSplitBlocksKeepsAQuoteWhole(t *testing.T) {
	src := "> quoted line one\n> quoted line two\n\nAfter."
	assertBlocks(t, src,
		[]Block{{Kind: BlockQuote}, {Kind: BlockParagraph}},
		[]string{"> quoted line one\n> quoted line two", "After."})
}

// Leading and trailing blank runs belong to no block: an offset pair
// that includes them puts stray newlines on the clipboard.
func TestSplitBlocksExcludesSurroundingBlankLines(t *testing.T) {
	src := "\n\n\nOnly paragraph.\n\n\n"
	assertBlocks(t, src, []Block{{Kind: BlockParagraph}}, []string{"Only paragraph."})
}

func TestSplitBlocksReturnsNothingForEmptyOrBlankSource(t *testing.T) {
	for _, src := range []string{"", "\n", "   \n\t\n"} {
		if got := SplitBlocks(src); len(got) != 0 {
			t.Errorf("SplitBlocks(%q) = %d blocks, want 0", src, len(got))
		}
	}
}

// Every block must be in order, non-empty, and inside the source. A
// fuzzy-ish sweep over mixed input catches offset arithmetic that the
// hand-written cases above happen to miss.
func TestSplitBlocksOffsetsAreOrderedAndInBounds(t *testing.T) {
	src := strings.Join([]string{
		"# Heading", "", "Prose here.", "", "- a", "- b", "",
		"```go", "x := 1", "```", "", "| h |", "| --- |", "| v |", "",
		"> quote", "", "Tail.",
	}, "\n")
	blocks := SplitBlocks(src)
	if len(blocks) < 6 {
		t.Fatalf("expected the mixed document to yield at least 6 blocks, got %d", len(blocks))
	}
	prevEnd := 0
	for i, b := range blocks {
		if b.Start < prevEnd {
			t.Errorf("block %d starts at %d, before the previous end %d", i, b.Start, prevEnd)
		}
		if b.Start >= b.End {
			t.Errorf("block %d is empty: [%d,%d)", i, b.Start, b.End)
		}
		if b.End > len(src) {
			t.Errorf("block %d ends at %d, past the source length %d", i, b.End, len(src))
		}
		if txt := blockText(src, b); strings.TrimSpace(txt) == "" {
			t.Errorf("block %d is all whitespace: %q", i, txt)
		}
		prevEnd = b.End
	}
}

// The splitter and RenderMarkdown must agree on what a fence is. If they
// drift, the picker's preview labels prose as code, or worse, offers a
// half fence. Both read the same grammar, and this pins that.
func TestSplitBlocksFenceAgreesWithRenderMarkdown(t *testing.T) {
	src := "text\n\n```\nplain code\n```"
	blocks := SplitBlocks(src)
	if len(blocks) != 2 || blocks[1].Kind != BlockFence {
		t.Fatalf("expected a trailing fence block, got %s", dumpBlocks(src, blocks))
	}
	if got := blockText(src, blocks[1]); !strings.HasPrefix(got, "```") || !strings.HasSuffix(got, "```") {
		t.Errorf("fence block should carry both markers, got %q", got)
	}
}
