package modes

import (
	"testing"

	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/tui"
)

func TestContextDialogTabSwitchAndClose(t *testing.T) {
	d := newContextDialog()
	d.Open("sid", "/path/sid.session", []string{"over1", "over2"}, []string{"ext1"})
	if !d.Active() {
		t.Fatal("should be active after Open")
	}
	if got := d.body(); len(got) != 2 || got[0] != "over1" {
		t.Fatalf("overview body = %v; want the overview slice", got)
	}
	d.HandleKey(tui.Key{Kind: tui.KeyTab})
	if got := d.body(); len(got) != 1 || got[0] != "ext1" {
		t.Fatalf("after Tab, body = %v; want the extensions slice", got)
	}
	d.HandleKey(tui.Key{Kind: tui.KeyTab}) // 2 tabs -> wraps back to overview
	if got := d.body(); got[0] != "over1" {
		t.Fatalf("Tab should wrap back to overview, got %v", got)
	}
	if closed := d.HandleKey(tui.Key{Kind: tui.KeyEsc}); !closed || d.Active() {
		t.Fatal("esc should close the dialog")
	}
}

func TestContextDialogScrollClamp(t *testing.T) {
	body := make([]string, 50)
	d := newContextDialog()
	d.Open("", "", body, nil)

	// Scroll up at the top stays at 0.
	d.HandleKey(tui.Key{Kind: tui.KeyUp})
	if d.scroll != 0 {
		t.Fatalf("scroll up at top = %d; want 0", d.scroll)
	}
	// Render clamps an over-scroll to the last full page.
	d.scroll = 1000
	_ = d.Render(tui.Theme{}, 80)
	if want := len(body) - contextBodyRows; d.scroll != want {
		t.Fatalf("scroll clamped to %d; want %d", d.scroll, want)
	}
}

func TestMessageKindAndBytes(t *testing.T) {
	tr := provider.Message{
		Role: provider.RoleTool,
		Content: []provider.Content{
			provider.ToolResultBlock{CallID: "x", Content: []provider.Content{provider.TextBlock{Text: "big result"}}},
		},
	}
	if got := messageKind(tr); got != "tool_result" {
		t.Errorf("messageKind = %q; want tool_result", got)
	}
	if messageBytes(tr) <= 0 {
		t.Error("messageBytes should be > 0 for a non-empty message")
	}

	asst := provider.Message{Role: provider.RoleAssistant, Content: []provider.Content{provider.TextBlock{Text: "hi"}}}
	if got := messageKind(asst); got != "assistant" {
		t.Errorf("plain assistant messageKind = %q; want assistant", got)
	}
}

func TestHumanBytes(t *testing.T) {
	cases := map[int]string{
		500:           "500 B",
		2048:          "2.0 KB",
		3 * (1 << 20): "3.0 MB",
	}
	for in, want := range cases {
		if got := humanBytes(in); got != want {
			t.Errorf("humanBytes(%d) = %q; want %q", in, got, want)
		}
	}
}
