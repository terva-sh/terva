package dialogs

import (
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/agent/modes/widgets"
	"terva.sh/terva/packages/testsupport"
	"terva.sh/terva/packages/tui"
)

func TestLoginDialogCursorPosMatchesPaddedInputRow(t *testing.T) {
	d := NewLoginDialog()
	d.Open(testsupport.TempDir(t))
	d.method = "oauth"
	d.provider = "anthropic"
	d.ShowStep(ctrlproto.AuthFlowStep{
		Flow: "f1", Kind: "form",
		Title: "Authorize terva, then paste the code",
		Lines: []string{"Open this page, approve, and paste back what it gives you."},
		URL:   "https://example.com/oauth/authorize?code_challenge=abc&state=xyz",
		Fields: []ctrlproto.AuthField{
			{Name: "code", Label: "Authorization code", Type: "secret", Required: true},
		},
	})

	lines := PadDialogFrame(d.Render(tui.Theme{}, 80))
	row, _ := d.CursorPos(80)
	if row < 0 || row >= len(lines) {
		t.Fatalf("CursorPos row = %d outside rendered lines %d", row, len(lines))
	}
	if got := widgets.StripANSIBytes(lines[row]); !strings.Contains(got, "▌") {
		t.Fatalf("CursorPos row %d = %q; want editor input row", row, got)
	}
}
