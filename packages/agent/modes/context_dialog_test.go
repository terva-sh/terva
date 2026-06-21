package modes

import (
	"strings"
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

func TestContextDialogWrapsLongLines(t *testing.T) {
	long := strings.Repeat("alpha beta ", 30) // ~330 chars on one logical line
	d := newContextDialog()
	d.Open("", "", []string{long, "", "short"}, nil)

	wrapped := d.wrappedBody(40) // limit = width-2 = 38
	if len(wrapped) < 2 {
		t.Fatalf("a %d-char line should wrap into multiple rows at width 40, got %d rows", len(long), len(wrapped))
	}
	for _, l := range wrapped {
		if len(l) > 38 { // ASCII body: byte len == display width
			t.Fatalf("wrapped row exceeds the 38-col limit: %d in %q", len(l), l)
		}
	}
	// The blank separator line survives wrapping as a blank line.
	blanks := 0
	for _, l := range wrapped {
		if l == "" {
			blanks++
		}
	}
	if blanks == 0 {
		t.Error("blank separator line was dropped by wrapping")
	}
}

func TestMessageKindCompaction(t *testing.T) {
	m := provider.Message{
		Role:    provider.RoleUser, // compaction summary is a synthetic user message
		Meta:    map[string]string{"compaction": "true"},
		Content: []provider.Content{provider.TextBlock{Text: "summary of earlier turns"}},
	}
	if got := messageKind(m); got != "compaction" {
		t.Errorf("messageKind(compaction summary) = %q; want compaction", got)
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
