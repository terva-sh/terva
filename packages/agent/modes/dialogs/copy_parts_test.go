package dialogs

import (
	"strings"
	"testing"

	"terva.sh/terva/packages/tui"
)

func TestSplitPartsStampsRoleAndMessageIndex(t *testing.T) {
	parts := SplitParts(RoleReply, 7, "One.\n\nTwo.")
	if len(parts) != 2 {
		t.Fatalf("got %d parts, want 2", len(parts))
	}
	for i, p := range parts {
		if p.Role != RoleReply {
			t.Errorf("part %d role = %v, want RoleReply", i, p.Role)
		}
		if p.MsgIdx != 7 {
			t.Errorf("part %d MsgIdx = %d, want 7", i, p.MsgIdx)
		}
	}
}

// Text is what reaches the clipboard, so it must be the author's own
// markdown: fence markers intact, interior blank lines preserved.
func TestSplitPartsTextIsVerbatimMarkdown(t *testing.T) {
	src := "Intro.\n\n```go\na := 1\n\nb := 2\n```"
	parts := SplitParts(RoleReply, 0, src)
	if len(parts) != 2 {
		t.Fatalf("got %d parts, want 2", len(parts))
	}
	want := "```go\na := 1\n\nb := 2\n```"
	if parts[1].Text != want {
		t.Errorf("fence text =\n%q\nwant\n%q", parts[1].Text, want)
	}
	if parts[1].Kind != tui.BlockFence {
		t.Errorf("kind = %v, want BlockFence", parts[1].Kind)
	}
}

// A picker row is one line. A preview carrying a newline would break the
// list layout, so no preview may contain one, whatever the block kind.
func TestSplitPartsPreviewIsAlwaysOneLine(t *testing.T) {
	src := strings.Join([]string{
		"# Heading", "", "Prose that runs", "over two lines.", "",
		"- item one", "- item two", "", "```sh", "just ci", "```", "",
		"| a | b |", "| --- | --- |", "| 1 | 2 |", "", "> quoted",
	}, "\n")
	parts := SplitParts(RoleReply, 0, src)
	if len(parts) < 5 {
		t.Fatalf("got %d parts, want at least 5", len(parts))
	}
	for i, p := range parts {
		if strings.ContainsAny(p.Preview, "\n\r") {
			t.Errorf("part %d (%v) preview contains a newline: %q", i, p.Kind, p.Preview)
		}
		if strings.TrimSpace(p.Preview) == "" {
			t.Errorf("part %d (%v) has an empty preview, text %q", i, p.Kind, p.Text)
		}
	}
}

func TestSplitPartsPreviewCollapsesProseToOneLine(t *testing.T) {
	parts := SplitParts(RoleReply, 0, "Prose that runs\nover two   lines.")
	if len(parts) != 1 {
		t.Fatalf("got %d parts, want 1", len(parts))
	}
	if want := "Prose that runs over two lines."; parts[0].Preview != want {
		t.Errorf("preview = %q, want %q", parts[0].Preview, want)
	}
}

// "```go" identifies nothing. The first line of actual code does.
func TestSplitPartsPreviewOfAFenceShowsTheFirstCodeLine(t *testing.T) {
	parts := SplitParts(RoleReply, 0, "```go\nfunc openJumpDialog() {}\n```")
	if len(parts) != 1 {
		t.Fatalf("got %d parts, want 1", len(parts))
	}
	if want := "func openJumpDialog() {}"; parts[0].Preview != want {
		t.Errorf("preview = %q, want %q", parts[0].Preview, want)
	}
}

// An empty fence still needs some label rather than a blank row.
func TestSplitPartsPreviewOfAnEmptyFenceFallsBackToTheLanguage(t *testing.T) {
	parts := SplitParts(RoleReply, 0, "```json\n```")
	if len(parts) != 1 {
		t.Fatalf("got %d parts, want 1", len(parts))
	}
	if strings.TrimSpace(parts[0].Preview) == "" {
		t.Fatal("empty fence produced an empty preview")
	}
}

func TestSplitPartsPreviewStripsMarkerSyntax(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want string
	}{
		{"heading", "## Segmentation\n\nBody text.", "Segmentation"},
		{"bullet", "- first item\n- second item", "first item"},
		{"numbered", "1. first item\n2. second item", "first item"},
		{"quote", "> quoted line\n> second line", "quoted line second line"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			parts := SplitParts(RoleReply, 0, c.src)
			if len(parts) == 0 {
				t.Fatal("no parts")
			}
			if parts[0].Preview != c.want {
				t.Errorf("preview = %q, want %q", parts[0].Preview, c.want)
			}
		})
	}
}

// A heading's preview is its own text, not the prose it absorbed: the
// heading is what the reader is scanning for. The prose still travels in
// Text, so the copy is complete.
func TestSplitPartsHeadingPreviewIgnoresTheAbsorbedProse(t *testing.T) {
	parts := SplitParts(RoleReply, 0, "## Where this plugs in\n\nA long paragraph follows here.")
	if len(parts) != 1 {
		t.Fatalf("got %d parts, want 1", len(parts))
	}
	if parts[0].Preview != "Where this plugs in" {
		t.Errorf("preview = %q", parts[0].Preview)
	}
	if !strings.Contains(parts[0].Text, "A long paragraph follows here.") {
		t.Errorf("absorbed prose missing from Text: %q", parts[0].Text)
	}
}

// Truncation is layout, and layout belongs to the picker, which alone
// knows its column width. The domain hands over the whole line.
func TestSplitPartsDoesNotTruncateThePreview(t *testing.T) {
	long := strings.Repeat("word ", 200)
	parts := SplitParts(RoleReply, 0, long)
	if len(parts) != 1 {
		t.Fatalf("got %d parts, want 1", len(parts))
	}
	if len(parts[0].Preview) < 900 {
		t.Errorf("preview was truncated to %d bytes; layout is the picker's job", len(parts[0].Preview))
	}
}

func TestSplitPartsReturnsNothingForBlankSource(t *testing.T) {
	for _, src := range []string{"", "\n\n", "   \t\n"} {
		if got := SplitParts(RoleThinking, 0, src); len(got) != 0 {
			t.Errorf("SplitParts(%q) = %d parts, want 0", src, len(got))
		}
	}
}

// Role identifiers are stable strings for tests and logs. The picker's
// visible labels are translated at the dialog layer, not here.
func TestPartRoleStringsAreStableIdentifiers(t *testing.T) {
	for role, want := range map[PartRole]string{
		RoleUser:     "user",
		RoleThinking: "thinking",
		RoleReply:    "reply",
	} {
		if got := role.String(); got != want {
			t.Errorf("role %d = %q, want %q", role, got, want)
		}
	}
}
