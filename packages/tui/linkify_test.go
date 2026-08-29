package tui

import (
	"strings"
	"testing"
)

func TestLinkifyURLs(t *testing.T) {
	enableHyperlinks(t)
	cases := []struct {
		name string
		in   string
		want string // the URL LinkifyURLs should have picked out, "" for none
	}{
		{"bare", "go to https://example.com now", "https://example.com"},
		{"http", "go to http://example.com now", "http://example.com"},
		{"end of line", "see https://example.com/a/b", "https://example.com/a/b"},
		{"trailing period", "see https://example.com/a.", "https://example.com/a"},
		{"trailing comma", "https://example.com/a, and", "https://example.com/a"},
		{"in parens", "(see https://example.com/a)", "https://example.com/a"},
		{"balanced parens kept", "https://example.com/foo_(bar)", "https://example.com/foo_(bar)"},
		{"query string", "https://x.example/?a=1&b=2#frag", "https://x.example/?a=1&b=2#frag"},
		{"mid-word is not a boundary", "notreallyhttps://example.com", ""},
		{"bare scheme is not a link", "https:// and text", ""},
		{"scheme plus punctuation is not a link", "ends with https://. done", ""},
		{"other schemes ignored", "file:///etc/passwd", ""},
		{"ftp ignored", "ftp://example.com/x", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := LinkifyURLs(tc.in)
			if tc.want == "" {
				if got != tc.in {
					t.Fatalf("LinkifyURLs(%q) = %q, want it left alone", tc.in, got)
				}
				return
			}
			if got == tc.in {
				t.Fatalf("LinkifyURLs(%q) linkified nothing", tc.in)
			}
			// The visible text is untouched; only the out-of-band target
			// is added. Anything else would change the layout.
			if plain := stripANSI(got); plain != tc.in {
				t.Errorf("visible text = %q, want %q", plain, tc.in)
			}
			if want := "\x1b]8;id=" + HyperlinkIDFor(tc.want) + ";" + tc.want + "\x1b\\"; !strings.Contains(got, want) {
				t.Errorf("LinkifyURLs(%q) = %q\nwant it to open %q", tc.in, got, want)
			}
		})
	}
}

// Running the linkifier over its own output must be a no-op: the visible
// text of a link is still a bare URL, and a second pass that linkified it
// again would nest one OSC 8 inside another.
func TestLinkifyIsIdempotent(t *testing.T) {
	enableHyperlinks(t)
	once := LinkifyURLs("see https://example.com/a and https://example.com/b")
	twice := LinkifyURLs(once)
	if once != twice {
		t.Errorf("second pass changed the string:\n once: %q\ntwice: %q", once, twice)
	}
}

func TestLinkifyHandlesSeveralURLsAndKeepsStyling(t *testing.T) {
	enableHyperlinks(t)
	th := Dark
	in := th.FG256(th.Accent, "https://a.example") + " and https://b.example"
	got := LinkifyURLs(in)
	if n := countLinkOpens(got); n != 2 {
		t.Errorf("opened %d links, want 2: %q", n, got)
	}
	if plain := stripANSI(got); plain != "https://a.example and https://b.example" {
		t.Errorf("visible text = %q", plain)
	}
}

// LinkifyURLs runs on every assistant line of every frame. A line with no
// "://" in it must come back as the identical string, not a rebuilt copy.
func TestLinkifyLeavesUnrelatedTextAlone(t *testing.T) {
	enableHyperlinks(t)
	for _, in := range []string{"", "plain prose", "  indented code = 1", "a:b c;d"} {
		if got := LinkifyURLs(in); got != in {
			t.Errorf("LinkifyURLs(%q) = %q", in, got)
		}
	}
}
