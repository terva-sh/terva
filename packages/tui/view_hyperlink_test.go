package tui

import (
	"strings"
	"testing"

	"terva.sh/terva/packages/provider"
)

func assistantView(text string) *View {
	return &View{
		Theme: Dark,
		Messages: []provider.Message{{
			Role:    provider.RoleAssistant,
			Content: []provider.Content{provider.TextBlock{Text: text}},
		}},
	}
}

// A URL in a reply is longer than the pane more often than not, and
// terva's wrap is what makes it unclickable: the terminal is handed two
// short strings where there was one URL. Linkifying before the wrap and
// carrying the link across the boundary is what fixes it, so the guard is
// that BOTH rows carry the whole target under one id.
func TestAssistantProseLinkifiesAcrossTheWrap(t *testing.T) {
	enableHyperlinks(t)
	const url = "https://terva.sh/docs/getting-started?ref=tui&utm_source=terminal"
	rows := assistantView("Docs are at " + url + " — see the auth section.").Build(48)

	var linked []string
	for _, r := range rows {
		if strings.Contains(r, "\x1b]8;") {
			linked = append(linked, r)
		}
	}
	if len(linked) < 2 {
		t.Fatalf("expected the URL to wrap over at least 2 linked rows, got %d:\n%q", len(linked), rows)
	}
	want := "\x1b]8;id=" + HyperlinkIDFor(url) + ";" + url + "\x1b\\"
	for n, r := range linked {
		if !strings.Contains(r, want) {
			t.Errorf("row %d does not carry the whole target: %q", n, r)
		}
	}
}

// The layout must not move. Everything else in the TUI is measured
// against these rows, and a link that cost a cell would show up as drift
// in tool boxes, the status band, and the scroll anchors.
func TestHyperlinksDoNotChangeVisibleLayout(t *testing.T) {
	const text = "See https://terva.sh/docs/a/very/long/path?x=1&y=2 and (https://example.com/b), plus https://example.com/c."

	SetHyperlinks(false)
	plain := assistantView(text).Build(48)
	enableHyperlinks(t)
	linked := assistantView(text).Build(48)

	if len(plain) != len(linked) {
		t.Fatalf("row count changed: %d -> %d", len(plain), len(linked))
	}
	for n := range plain {
		if p, l := stripANSI(plain[n]), stripANSI(linked[n]); p != l {
			t.Errorf("row %d visible text changed:\n  off: %q\n   on: %q", n, p, l)
		}
	}
}

// renderMessageCached memoises by message+width+view state. The
// hyperlink flag is view state that renderMessage reads, so a render made
// with it off is not interchangeable with one made with it on — which is
// exactly the stale row this returned before the flag joined the key.
func TestRenderCacheKeyedOnHyperlinkState(t *testing.T) {
	v := assistantView("Docs: https://terva.sh/docs")
	v.renderCache = map[msgCacheKey][]string{}

	SetHyperlinks(false)
	if got := strings.Join(v.Build(60), "\n"); strings.Contains(got, "\x1b]8;") {
		t.Fatalf("emitted a hyperlink with the flag off: %q", got)
	}
	enableHyperlinks(t)
	if got := strings.Join(v.Build(60), "\n"); !strings.Contains(got, "\x1b]8;") {
		t.Errorf("served a cached un-linkified render after the flag went on: %q", got)
	}
}
