package modes

import (
	"strings"
	"testing"

	"terva.sh/terva/packages/tui"
)

func TestLoginDialogCursorPosMatchesPaddedInputRow(t *testing.T) {
	d := newLoginDialog()
	d.Open(t.TempDir())
	d.method = "oauth"
	d.provider = "anthropic"
	d.ShowWaiting("https://example.com/oauth/authorize?code_challenge=abc&state=xyz")

	lines := padDialogFrame(d.Render(tui.Theme{}, 80))
	row, _ := d.CursorPos(80)
	if row < 0 || row >= len(lines) {
		t.Fatalf("CursorPos row = %d outside rendered lines %d", row, len(lines))
	}
	if got := stripANSIBytes(lines[row]); !strings.Contains(got, "▌") {
		t.Fatalf("CursorPos row %d = %q; want editor input row", row, got)
	}
}
