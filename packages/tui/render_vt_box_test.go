package tui_test

// End-to-end proof (through a real VT emulator) that streaming tool
// progress containing tabs cannot desync the frame. vt10x expands tabs
// to real tab stops the way a hardware terminal does, so before the
// normalization fix this test shows the box's right border pushed past
// its column (or wrapped onto an extra row) — the first domino in the
// interleaved-frame corruption seen during long `go test` runs.

import (
	"encoding/json"
	"strings"
	"testing"

	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/tui"
)

func TestStreamingProgressWithTabsKeepsGridAligned(t *testing.T) {
	ft, r := newVTRenderer(t, 80, 24, "")

	args := json.RawMessage(`{"command":"go test ./..."}`)
	v := tui.View{
		Theme: tui.Dark,
		Messages: []provider.Message{{
			Role:    provider.RoleAssistant,
			Content: []provider.Content{provider.ToolCallBlock{ID: "toolu_1", Name: "bash", Arguments: args}},
		}},
		ToolCalls: []tui.ToolCallView{{
			ID:   "toolu_1",
			Name: "bash",
			Args: tui.ShortArgs("bash", args),
			Progress: "ok  \tterva.sh/terva/e2e\t5.800s\n" +
				"ok  \tterva.sh/terva/packages/agent\t13.549s",
		}},
	}

	r.DrawLog(v.Build(80), []string{"> "}, 0, 2)

	// Every bordered row must close its right edge in the same column:
	// total box span is the full 80-cell width with a 2-cell outer
	// margin, so after Rows() right-trims trailing blanks the closing
	// border sits at cell 77 (0-based). A raw tab in a body row makes
	// the emulator expand to the next tab stop and pushes the border
	// off that column or wraps the row.
	const wantBorderCol = 77
	sawBox := false
	for y, row := range ft.Screen().Rows() {
		if !strings.ContainsAny(row, "│┌└") {
			continue
		}
		sawBox = true
		cells := []rune(row) // box + go-test content are all width-1 runes
		col := -1
		for i := len(cells) - 1; i >= 0; i-- {
			if cells[i] == '│' || cells[i] == '┐' || cells[i] == '┘' {
				col = i
				break
			}
		}
		if col != wantBorderCol {
			t.Fatalf("grid row %d: right border at cell %d, want %d\nrow: %q\nfull grid:\n%s",
				y, col, wantBorderCol, row, ft.Screen().Text())
		}
	}
	if !sawBox {
		t.Fatal("expected a tool box on the grid")
	}
}
