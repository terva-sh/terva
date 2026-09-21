package testsupport

import (
	"os"
	"path/filepath"
	"testing"
)

// TestDocAnchorSlugify pins the derivation, because the way to get this wrong
// produces a gate that looks right and reports healthy links as broken.
func TestDocAnchorSlugify(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Goal", "goal"},
		{"Resolved design decisions", "resolved-design-decisions"},

		// The one that matters here. An em dash is dropped and the spaces
		// around it are not, so the anchor carries two hyphens. A slugger that
		// collapses whitespace reports eight healthy links in docs/ as broken,
		// which is how this test earned its place.
		{"Carriers — current state", "carriers--current-state"},
		{"Security — authority", "security--authority"},
		{"Responsible use: context & tools", "responsible-use-context--tools"},

		// Punctuation goes, the word characters stay, and an underscore is a
		// word character: docs/extension-protocol.md names `panel_resize`.
		{"`panel_resize`", "panel_resize"},
		{"G1. The note gets answered", "g1-the-note-gets-answered"},
		{"Swarm sub-agent tiers (weak / medium / strong)", "swarm-sub-agent-tiers-weak--medium--strong"},
		{"Swarm sub-agent tiers (weak / medium / strong / cheap)", "swarm-sub-agent-tiers-weak--medium--strong--cheap"},

		// A rename sweep marks lines it must not touch. The marker is not
		// heading text and no renderer reads an anchor out of it. A comment
		// between two words leaves the spaces either side, the same way an em
		// dash does.
		{"Lineage <!-- rename:keep -->", "lineage"},
		{"Fork <!-- rename:keep --> policy", "fork--policy"},
	}
	for _, c := range cases {
		if got := slugify(c.in); got != c.want {
			t.Errorf("slugify(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// Two sections with the same title are two anchors, and a renderer numbers the
// second. A gate that folded them would pass a link to `#notes-1` on a page
// that has only one Notes section.
func TestDocAnchorDuplicateHeadings(t *testing.T) {
	got := headingSlugs("# Notes\n\n## Notes\n\n### Notes\n")
	for _, want := range []string{"notes", "notes-1", "notes-2"} {
		if !got[want] {
			t.Errorf("missing anchor %q from three Notes headings: %v", want, got)
		}
	}
	if got["notes-3"] {
		t.Error("invented a fourth anchor from three headings")
	}
}

// A "#" in a shell sample is a comment, and a link in a code block is text to
// read rather than a link to follow. Reporting either teaches the reader to
// ignore this gate.
func TestDocAnchorFencedCodeIsSkipped(t *testing.T) {
	const src = "# Real\n\n```sh\n# Not a heading\nsee [x](other.md#nowhere)\n```\n\n~~~\n## Also not\n~~~\n"
	slugs := headingSlugs(src)
	if !slugs["real"] {
		t.Error("the heading outside the fence was missed")
	}
	if slugs["not-a-heading"] || slugs["also-not"] {
		t.Errorf("a comment inside a fence registered as a heading: %v", slugs)
	}
	if links := markdownLinks(src); len(links) != 0 {
		t.Errorf("a sample link inside a fence was collected: %v", links)
	}
}

// TestDocAnchorGateSeesTheKnownBreakages: the three dead anchors TKT-01M2JZ8W4
// found in shipped pages. All three are repaired, so this asserts the property
// that makes the gate worth having rather than re-reporting them — for each,
// the page the link NAMED no longer offers the fragment, and the page the
// content moved to does.
//
// That pair is the whole mechanism. check-links passes every one of these
// because the named file still exists.
func TestDocAnchorGateSeesTheKnownBreakages(t *testing.T) {
	root := filepath.Join("..", "..")
	cases := []struct {
		named, moved, anchor, nowAt string
	}{
		// The cli.md split moved the section to its own page. This one sat
		// dead through three later pull requests.
		{"docs/cli.md", "docs/secrets.md", "secrets-at-rest-terva-secret", "secrets-at-rest-terva-secret"},
		// The extensions.md split moved the message reference out.
		{"docs/extensions.md", "docs/extension-protocol.md", "panel_resize", "panel_resize"},
		// No split here: the heading gained "/ cheap", and the anchor changed
		// with it.
		{"docs/models.md", "docs/models.md", "swarm-sub-agent-tiers-weak--medium--strong", "swarm-sub-agent-tiers-weak--medium--strong--cheap"},
	}
	for _, c := range cases {
		named := readSlugs(t, root, c.named)
		if named[c.anchor] {
			t.Errorf("%s still offers %q — this case no longer tests what it claims to", c.named, c.anchor)
		}
		if moved := readSlugs(t, root, c.moved); !moved[c.nowAt] {
			t.Errorf("%s does not offer %q — the content moved again and this case needs updating", c.moved, c.nowAt)
		}
	}
}

func readSlugs(t *testing.T, root, rel string) map[string]bool {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return headingSlugs(string(body))
}
