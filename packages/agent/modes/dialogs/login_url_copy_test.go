package dialogs

import (
	"path/filepath"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/agent/modes/widgets"
	"terva.sh/terva/packages/provider/auth"
	"terva.sh/terva/packages/testsupport"
	"terva.sh/terva/packages/tui"
)

const wrappingAuthURL = "https://auth.example.com/oauth/authorize?client_id=terva&code_challenge=abcdefghijklmnop&state=0123456789abcdef"

func newFlowDialog(t *testing.T, step ctrlproto.AuthFlowStep) *LoginDialog {
	t.Helper()
	d := NewLoginDialog()
	d.Open(auth.NewStore(filepath.Join(testsupport.TempDir(t), "auth.json")))
	d.method = "oauth"
	d.provider = "anthropic"
	d.ShowStep(step)
	return d
}

func enableHyperlinks(t *testing.T) {
	t.Helper()
	prev := tui.HyperlinksEnabled()
	tui.SetHyperlinks(true)
	t.Cleanup(func() { tui.SetHyperlinks(prev) })
}

// The wrapped login URL must reach the terminal as ONE hyperlink. Before
// this, terva's own wrap split the URL into rows the terminal could only
// read as separate short strings, so cmd+click did nothing and a mouse
// selection came back with terva's newlines in the middle of the URL.
func TestLoginURLWrapsAsOneHyperlink(t *testing.T) {
	enableHyperlinks(t)
	d := newFlowDialog(t, ctrlproto.AuthFlowStep{
		Flow: "f1", Kind: "display", Title: "approve in your browser", URL: wrappingAuthURL,
	})

	const width = 48
	lines := d.Render(tui.Theme{}, width)
	var urlRows []string
	for _, l := range lines {
		if strings.Contains(l, "\x1b]8;") {
			urlRows = append(urlRows, l)
		}
	}
	if len(urlRows) < 2 {
		t.Fatalf("expected the URL to wrap over several rows, got %d:\n%q", len(urlRows), lines)
	}
	id := tui.HyperlinkIDFor(wrappingAuthURL)
	for n, row := range urlRows {
		if !strings.Contains(row, "\x1b]8;id="+id+";"+wrappingAuthURL+"\x1b\\") {
			t.Errorf("row %d does not carry the full target with id %q: %q", n, id, row)
		}
		if !strings.HasSuffix(row, tui.HyperlinkClose) {
			t.Errorf("row %d leaves the link open: %q", n, row)
		}
	}
	if got := widgets.StripANSIBytes(strings.Join(urlRows, "")); got != wrappingAuthURL {
		t.Errorf("rows reassemble to %q, want the URL", got)
	}
}

// Row count must not change: CursorPos counts the URL's rows to find the
// focused editor, and a hyperlink that cost a row would put the caret on
// the wrong line.
func TestHyperlinkedURLKeepsRowCountAndWidth(t *testing.T) {
	step := ctrlproto.AuthFlowStep{
		Flow: "f1", Kind: "form", Title: "paste the code", URL: wrappingAuthURL,
		Fields: []ctrlproto.AuthField{{Name: "code", Label: "Authorization code", Type: "secret", Required: true}},
	}
	const width = 48

	plain := newFlowDialog(t, step).Render(tui.Theme{}, width)
	enableHyperlinks(t)
	linked := newFlowDialog(t, step).Render(tui.Theme{}, width)

	if len(plain) != len(linked) {
		t.Fatalf("row count changed: %d without hyperlinks, %d with", len(plain), len(linked))
	}
	for n := range plain {
		if p, l := widgets.StripANSIBytes(plain[n]), widgets.StripANSIBytes(linked[n]); p != l {
			t.Errorf("row %d visible text changed:\n plain: %q\nlinked: %q", n, p, l)
		}
	}
}

// 'c' copies on a step with nothing to type into, and is a literal 'c' on
// a step with fields — a shortcut that ate a character of someone's API
// key would be worse than no shortcut.
func TestCopyURLKeyOnlyWhenNothingIsBeingTyped(t *testing.T) {
	display := newFlowDialog(t, ctrlproto.AuthFlowStep{
		Flow: "f1", Kind: "display", Title: "approve in your browser", URL: wrappingAuthURL,
	})
	if act := display.HandleKey(tui.Key{Kind: tui.KeyRune, Rune: 'c'}); !act.CopyURL {
		t.Errorf("c on a display step: CopyURL = false, want true")
	}
	if got := display.FlowURL(); got != wrappingAuthURL {
		t.Errorf("FlowURL = %q, want the step's URL", got)
	}

	form := newFlowDialog(t, ctrlproto.AuthFlowStep{
		Flow: "f1", Kind: "form", Title: "paste the code", URL: wrappingAuthURL,
		Fields: []ctrlproto.AuthField{{Name: "code", Label: "Authorization code", Type: "text", Required: true}},
	})
	form.editor(tui.Theme{}, 0) // the dialog builds editors lazily, at render
	if act := form.HandleKey(tui.Key{Kind: tui.KeyRune, Rune: 'c'}); act.CopyURL {
		t.Fatalf("c on a form step reported CopyURL; it must reach the field")
	}
	if got := form.fieldValue(0); got != "c" {
		t.Errorf("field value = %q, want the typed %q", got, "c")
	}

	noURL := newFlowDialog(t, ctrlproto.AuthFlowStep{Flow: "f1", Kind: "display", Title: "waiting"})
	if act := noURL.HandleKey(tui.Key{Kind: tui.KeyRune, Rune: 'c'}); act.CopyURL {
		t.Errorf("c with no URL reported CopyURL")
	}
}

func TestLoginNoticeRenders(t *testing.T) {
	d := newFlowDialog(t, ctrlproto.AuthFlowStep{
		Flow: "f1", Kind: "display", Title: "approve in your browser", URL: wrappingAuthURL,
	})
	d.Notice("copied the url", false)
	if !strings.Contains(widgets.StripANSIBytes(strings.Join(d.Render(tui.Theme{}, 60), "\n")), "copied the url") {
		t.Errorf("notice not rendered")
	}
	// A stale "copied" sitting over a fresh form would read as a claim
	// about the form, so a new step clears it.
	d.ShowStep(ctrlproto.AuthFlowStep{Flow: "f2", Kind: "display", Title: "next"})
	if strings.Contains(widgets.StripANSIBytes(strings.Join(d.Render(tui.Theme{}, 60), "\n")), "copied the url") {
		t.Errorf("notice survived a new step")
	}
}
