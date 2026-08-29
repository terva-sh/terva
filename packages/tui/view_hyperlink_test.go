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

// expandedReasoning is reasoningView (view_reasoning_test.go) with the
// thinking already unfolded — the state these two care about, since the
// collapsed arm is a single marker row with nothing to wrap or link.
func expandedReasoning(summary string) *View {
	v := reasoningView(summary, "done")
	v.ExpandAll = true
	return &v
}

// Recorded thinking is model prose like any other, so a URL in it should
// click like one in the reply. Before this it could not: the rows were
// emitted unwrapped, so there was no wrap boundary to carry a link
// across — and the row never reached the wrap in the first place.
func TestReasoningRowsLinkifyAcrossTheWrap(t *testing.T) {
	enableHyperlinks(t)
	const url = "https://terva.sh/docs/getting-started?ref=tui&utm_source=terminal"
	rows := expandedReasoning("I should check " + url + " before answering.").Build(48)

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

// The row that made the linkifying pointless: RenderMarkdown's width
// argument only draws rules, so an un-wrapped paragraph of thinking left
// here as one 160-cell row and the renderer cut it at the pane edge. The
// tail was not folded onto the next line — it was dropped.
func TestReasoningRowsWrapToThePane(t *testing.T) {
	const width = 48
	long := "I should check the docs before answering, because the auth section covers this exact case and the wording matters quite a lot here."
	rows := expandedReasoning(long).Build(width)

	body := 0
	for _, r := range rows {
		if w := visibleWidth(r); w > width {
			t.Errorf("row is %d cells wide in a %d-cell pane: %q", w, width, stripANSI(r))
		}
		if strings.Contains(stripANSI(r), "auth section") || strings.Contains(stripANSI(r), "wording matters") {
			body++
		}
	}
	// The tail has to actually be present, not merely within width: a
	// truncating renderer would also pass the width check above.
	if body < 2 {
		t.Errorf("the paragraph did not wrap onto further rows (%d body rows):\n%q", body, rows)
	}
}
