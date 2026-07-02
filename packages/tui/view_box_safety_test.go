package tui

// Regression tests for the live-overlay sanitization hole behind the
// "broken boxes" frame corruption: bash progress chunks (EvToolProgress)
// and live Result overlays rendered raw subprocess output — tabs, CRs,
// escape sequences — straight into tool-box rows. A tab in a box row
// makes the terminal draw up to 8 cells where visibleWidth counted 0,
// so the padded row exceeds the real terminal width, wraps, and desyncs
// DrawLog's open-loop cursor tracking; every row after that paints in
// the wrong place. The fix routes every live/plain body through
// normalizeBashOutputLines. These tests pin that invariant.

import (
	"encoding/json"
	"strings"
	"testing"

	"terva.sh/terva/packages/provider"
)

// dirtyToolOutput mimics go test / subprocess output: tab-separated
// columns, an ANSI-colored row, a CR progress overwrite, and a BEL.
const dirtyToolOutput = "ok  \tterva.sh/terva/e2e\t5.800s\n" +
	"\x1b[31mFAIL\x1b[0m\tterva.sh/terva/packages/agent\t13.549s\n" +
	"downloading 50%\rdownloading 100%\x07"

// assertBoxSafeRows fails if any rendered row still carries a byte that
// a terminal would interpret as cursor movement or variable-width fill.
// stripANSI removes the CSI styling the theme itself adds; anything
// left (raw tab, CR, BS, BEL, or a non-CSI escape from tool output)
// is exactly the class of byte that corrupts the frame.
func assertBoxSafeRows(t *testing.T, rows []string) {
	t.Helper()
	for i, row := range rows {
		plain := stripANSI(row)
		for _, bad := range []struct{ name, s string }{
			{"tab", "\t"},
			{"carriage return", "\r"},
			{"backspace", "\b"},
			{"bell", "\x07"},
			{"escape", "\x1b"},
		} {
			if strings.Contains(plain, bad.s) {
				t.Fatalf("row %d carries a raw %s after stripANSI: %q", i, bad.name, row)
			}
		}
	}
}

// boxRowsHaveUniformWidth asserts every bordered row spans exactly
// width cells, so the right edge forms a straight column.
func boxRowsHaveUniformWidth(t *testing.T, rows []string, width int) {
	t.Helper()
	for i, row := range rows {
		if !strings.Contains(row, "│") && !strings.Contains(row, "┌") && !strings.Contains(row, "└") {
			continue
		}
		if w := visibleWidth(row); w != width {
			t.Fatalf("box row %d spans %d cells, want %d: %q", i, w, width, stripANSI(row))
		}
	}
}

func liveBashView(tc ToolCallView) View {
	args := json.RawMessage(`{"command":"go test ./..."}`)
	tc.ID = "toolu_1"
	tc.Name = "bash"
	tc.Args = ShortArgs("bash", args)
	return View{
		Theme: Dark,
		Messages: []provider.Message{{
			Role:    provider.RoleAssistant,
			Content: []provider.Content{provider.ToolCallBlock{ID: "toolu_1", Name: "bash", Arguments: args}},
		}},
		ToolCalls: []ToolCallView{tc},
	}
}

// The in-flight progress body (EvToolProgress carries raw bash chunks)
// must be normalized before it reaches box rows.
func TestLiveProgressBodyIsBoxSafe(t *testing.T) {
	v := liveBashView(ToolCallView{Progress: dirtyToolOutput})
	rows := v.Build(80)
	assertBoxSafeRows(t, rows)
	boxRowsHaveUniformWidth(t, rows, 80)
	plain := stripANSI(strings.Join(rows, "\n"))
	if !strings.Contains(plain, "terva.sh/terva/e2e") {
		t.Fatalf("normalization dropped the progress content:\n%s", plain)
	}
	// The CR row splits into its two progress states instead of
	// overwriting in place.
	if !strings.Contains(plain, "downloading 100%") {
		t.Fatalf("CR-separated progress tail missing:\n%s", plain)
	}
}

// The live Result overlay (shown between EvToolResult and the result
// finalising into the transcript) renders the same raw text and needs
// the same normalization.
func TestLiveResultOverlayIsBoxSafe(t *testing.T) {
	v := liveBashView(ToolCallView{Result: "$ go test ./...\n" + dirtyToolOutput, Done: true})
	rows := v.Build(80)
	assertBoxSafeRows(t, rows)
	boxRowsHaveUniformWidth(t, rows, 80)
}

// Finalised results from non-bash tools (grep, MCP servers, extensions)
// take renderToolText's plain branch, which historically wrapped the
// text verbatim. Grep output quoting Go source is full of tabs.
func TestFinalisedPlainToolResultIsBoxSafe(t *testing.T) {
	args := json.RawMessage(`{"pattern":"func"}`)
	v := View{
		Theme: Dark,
		Messages: []provider.Message{
			{
				Role:    provider.RoleAssistant,
				Content: []provider.Content{provider.ToolCallBlock{ID: "toolu_1", Name: "grep", Arguments: args}},
			},
			{
				Role: provider.RoleTool,
				Content: []provider.Content{provider.ToolResultBlock{
					CallID:  "toolu_1",
					Content: []provider.Content{provider.TextBlock{Text: "main.go:12:\tfunc main() {\nutil.go:3:\tfunc helper() {"}},
				}},
			},
		},
	}
	rows := v.Build(80)
	assertBoxSafeRows(t, rows)
	boxRowsHaveUniformWidth(t, rows, 80)
	plain := stripANSI(strings.Join(rows, "\n"))
	if !strings.Contains(plain, "func main() {") {
		t.Fatalf("normalization dropped grep content:\n%s", plain)
	}
}
