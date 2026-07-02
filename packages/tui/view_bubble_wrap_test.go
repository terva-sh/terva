package tui

import (
	"strings"
	"testing"

	"terva.sh/terva/packages/provider"
)

// A long user message must wrap into multiple bubble rows, each within
// the terminal width — never a single row that runs off the right edge
// and gets clipped by the renderer's hard truncation.
func TestUserBubbleWrapsLongMessage(t *testing.T) {
	const width = 60
	msg := "Interesting that it has no directory set... I wonder if I did a " +
		"local install and lost the config? I thought that was fixed by using " +
		"the data directory but it is also possible something else has changed here."
	v := View{
		Theme: Dark,
		Messages: []provider.Message{{
			Role:    provider.RoleUser,
			Content: []provider.Content{provider.TextBlock{Text: msg}},
		}},
	}

	rows := v.Build(width)
	content := 0
	for i, row := range rows {
		if w := visibleWidth(row); w > width {
			t.Fatalf("row %d spans %d cells, wider than the %d-cell terminal: %q",
				i, w, width, stripANSI(row))
		}
		if strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(stripANSI(row)), "▌")) != "" {
			content++
		}
	}
	if content < 4 {
		t.Fatalf("expected the message to wrap across several bubble rows, got %d:\n%s",
			content, stripANSI(strings.Join(rows, "\n")))
	}
	// Nothing may be silently dropped: rejoin the bubble text and check
	// the tail of the message survived wrapping.
	plain := stripANSI(strings.Join(rows, "\n"))
	if !strings.Contains(plain, "something else has changed here.") {
		t.Fatalf("tail of the user message lost in wrapping:\n%s", plain)
	}
}
