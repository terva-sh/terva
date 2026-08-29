package tui

import (
	"strings"
	"testing"
)

// enableHyperlinks turns OSC 8 emission on for one test and restores the
// process default afterwards. The default is OFF precisely so that every
// other rendering test in this package keeps asserting the same bytes on
// a developer's iTerm2 and on a CI runner with no TERM_PROGRAM.
func enableHyperlinks(t *testing.T) {
	t.Helper()
	prev := HyperlinksEnabled()
	SetHyperlinks(true)
	t.Cleanup(func() { SetHyperlinks(prev) })
}

func TestHyperlinksOffByDefault(t *testing.T) {
	if HyperlinksEnabled() {
		t.Fatal("hyperlinks are on by default; every golden in this package would then depend on the host terminal")
	}
	const url = "https://example.com/auth"
	if got := Hyperlink(url, url); got != url {
		t.Errorf("Hyperlink with emission off = %q, want the bare text", got)
	}
	if got := LinkifyURLs("see " + url); got != "see "+url {
		t.Errorf("LinkifyURLs with emission off = %q, want unchanged", got)
	}
}

func TestHyperlinkEmitsOSC8(t *testing.T) {
	enableHyperlinks(t)
	got := HyperlinkID("https://example.com/x", "abc", "click")
	want := "\x1b]8;id=abc;https://example.com/x\x1b\\click\x1b]8;;\x1b\\"
	if got != want {
		t.Errorf("HyperlinkID = %q, want %q", got, want)
	}
}

// A link target reaches us from model prose as well as our own dialogs,
// and the OSC payload ends at the first BEL or ST. A control byte in the
// URL would close the sequence early and spill the rest onto the screen,
// so the whole link is declined rather than emitted half-formed.
func TestHyperlinkRefusesUnsafeURLs(t *testing.T) {
	enableHyperlinks(t)
	for name, url := range map[string]string{
		"bel":       "https://example.com/\x07evil",
		"esc":       "https://example.com/\x1b]0;title\x07",
		"newline":   "https://example.com/\nrest",
		"del":       "https://example.com/\x7f",
		"too-long":  "https://example.com/" + strings.Repeat("a", maxHyperlinkURL),
		"empty-url": "",
	} {
		t.Run(name, func(t *testing.T) {
			got := Hyperlink(url, "text")
			if strings.Contains(got, "\x1b]8;") {
				t.Errorf("emitted an OSC 8 for %q: %q", url, got)
			}
			if got != "text" {
				t.Errorf("got %q, want the bare text", got)
			}
		})
	}
}

// The whole reason the width paths learned about OSC: a hyperlink adds
// bytes and no cells. If visibleWidth counted the target, every layout
// decision around a linkified line would be wrong by the length of a URL.
func TestHyperlinkIsZeroWidth(t *testing.T) {
	enableHyperlinks(t)
	linked := Hyperlink("https://example.com/a/very/long/path?with=query&more=stuff", "example.com")
	if got, want := visibleWidth(linked), visibleWidth("example.com"); got != want {
		t.Errorf("visibleWidth(linked) = %d, want %d", got, want)
	}
	if got := stripANSI(linked); got != "example.com" {
		t.Errorf("stripANSI = %q, want %q", got, "example.com")
	}
}

// A row cut inside a link must not leave the link open: an unterminated
// OSC 8 target claims whatever the terminal draws next.
func TestTruncateClosesAnOpenHyperlink(t *testing.T) {
	enableHyperlinks(t)
	line := Hyperlink("https://example.com/x", "abcdefghij")
	got := truncateToWidth(line, 4)
	if w := visibleWidth(got); w != 4 {
		t.Fatalf("visible width = %d, want 4 (%q)", w, got)
	}
	if opens, closes := countLinkOpens(got), strings.Count(got, hyperlinkClose); opens != closes {
		t.Errorf("truncated row has %d link opens and %d closes: %q", opens, closes, got)
	}
}

func TestLinkStateAfter(t *testing.T) {
	open := "\x1b]8;id=x;https://example.com\x1b\\"
	if got := linkStateAfter("", open+"text"); got != open {
		t.Errorf("after an open, state = %q, want %q", got, open)
	}
	if got := linkStateAfter(open, "more"+hyperlinkClose); got != "" {
		t.Errorf("after a close, state = %q, want empty", got)
	}
	// The close form may carry an id of its own; what marks it a close is
	// the empty URI, not the sequence being byte-equal to hyperlinkClose.
	if got := linkStateAfter(open, "\x1b]8;id=x;\x1b\\"); got != "" {
		t.Errorf("id-carrying close left state %q, want empty", got)
	}
}

// The point of the exercise: a URL too long for the row still clicks,
// because every row repeats the opening sequence — id and all — so the
// terminal knows the rows are one link rather than three short ones.
func TestWrapKeepsOneHyperlinkAcrossRows(t *testing.T) {
	enableHyperlinks(t)
	url := "https://auth.example.com/authorize?client_id=abcdef&scope=all&state=0123456789"
	rows := wrapANSILineKeepStyle(Hyperlink(url, url), 20)
	if len(rows) < 3 {
		t.Fatalf("expected the URL to wrap over several rows, got %d", len(rows))
	}
	id := HyperlinkIDFor(url)
	for n, row := range rows {
		if !strings.Contains(row, "id="+id+";") {
			t.Errorf("row %d does not re-open the link with id %q: %q", n, id, row)
		}
		if opens, closes := countLinkOpens(row), strings.Count(row, hyperlinkClose); opens != closes {
			t.Errorf("row %d has %d opens and %d closes: %q", n, opens, closes, row)
		}
		if w := visibleWidth(row); w > 20 {
			t.Errorf("row %d is %d cells wide, want <= 20: %q", n, w, row)
		}
	}
	if got := stripANSI(strings.Join(rows, "")); got != url {
		t.Errorf("rows reassemble to %q, want the URL back", got)
	}
}

// With emission off the wrap must be byte-identical to what it produced
// before hyperlinks existed — that is what makes this safe to leave on
// the assistant render path.
func TestWrapUnchangedWithoutHyperlinks(t *testing.T) {
	url := "https://auth.example.com/authorize?client_id=abcdef&scope=all&state=0123456789"
	plain := wrapANSILineKeepStyle(url, 20)
	linked := wrapANSILineKeepStyle(LinkifyURLs(url), 20)
	if strings.Join(plain, "\n") != strings.Join(linked, "\n") {
		t.Errorf("wrap changed with emission off:\n%q\n%q", plain, linked)
	}
}

// countLinkOpens counts OSC 8 sequences that OPEN a link (non-empty URI),
// which is what has to balance against the closes on a self-contained row.
func countLinkOpens(s string) int {
	n := 0
	for i := 0; i < len(s); {
		l := escSeqLen(s, i)
		if l == 0 {
			i++
			continue
		}
		seq := s[i : i+l]
		if strings.HasPrefix(seq, "\x1b]8;") && !isHyperlinkClose(seq) {
			n++
		}
		i += l
	}
	return n
}
