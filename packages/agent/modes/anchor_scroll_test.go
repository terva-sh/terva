package modes

import "testing"

func TestAnchorScrollOffsetKeepsTopVisibleRow(t *testing.T) {
	// The user is scrolled up. The top visible row of the chat window is
	// start = chatLen - offset - chatRows. After any redraw that grows
	// the buffer and/or changes the viewport height, the SAME top row
	// must remain visible.
	cases := []struct {
		name                                       string
		offset, prevLen, newLen, prevRows, newRows int
	}{
		{"agent appends streamed lines", 20, 200, 208, 30, 30},
		{"bottom band grows (chatRows shrinks)", 20, 200, 200, 30, 26},
		{"streamed text plus growing status band", 20, 200, 205, 30, 27},
		{"buffer shrinks when streaming block finalises", 20, 200, 196, 30, 30},
		{"both grow", 5, 100, 140, 20, 24},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			startBefore := c.prevLen - c.offset - c.prevRows
			got := anchorScrollOffset(c.offset, c.prevLen, c.newLen, c.prevRows, c.newRows)
			startAfter := c.newLen - got - c.newRows
			if startAfter != startBefore {
				t.Fatalf("top row drifted: before=%d after=%d (offset %d->%d)",
					startBefore, startAfter, c.offset, got)
			}
		})
	}
}

func TestAnchorScrollOffsetClampsToZero(t *testing.T) {
	// A large negative adjustment (buffer shrank a lot) must clamp at 0
	// rather than going negative.
	if got := anchorScrollOffset(3, 200, 100, 30, 30); got != 0 {
		t.Fatalf("offset = %d; want 0", got)
	}
}

func TestAnchorScrollOffsetClampsToLen(t *testing.T) {
	if got := anchorScrollOffset(5, 10, 20, 100, 8); got > 20 {
		t.Fatalf("offset = %d; want <= newLen 20", got)
	}
}
