package tui

// SanitizeLabel and the two card fields that go through it.
//
// A shared file's name and caption are the only strings in a card that this
// process did not write. The caption is chosen by the MODEL, so a
// prompt-injected agent authors it; on `terva attach` both also arrive from a
// daemon on another machine. They are then painted inside a row that has its
// own colour and alignment, which is what turns "a mangled name" into "a
// mangled frame".
//
// The card assertions count escape bytes against a benign control render rather
// than looking for specific sequences. The theme emits escapes of its own, so
// the honest question is not "are there escapes in this row" but "did the label
// contribute any", and only a comparison answers that.

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"terva.sh/terva/packages/provider"
)

func TestSanitizeLabel(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain text is untouched", "report.pdf", "report.pdf"},
		{"empty stays empty", "", ""},
		{"unicode survives", "rapport-café-日本語.pdf", "rapport-café-日本語.pdf"},

		// The whole sequence goes, not just the ESC. Dropping the escape byte
		// alone would print "[31m" as visible text.
		{"CSI colour", "ok\x1b[31mBAD", "okBAD"},
		{"CSI cursor move", "a\x1b[2Jb", "ab"},
		{"OSC title setter", "a\x1b]0;pwned\x07b", "ab"},
		{"OSC terminated by ST", "a\x1b]0;pwned\x1b\\b", "ab"},
		{"two-byte escape", "a\x1b7b", "ab"},
		{"bare trailing ESC", "a\x1b", "a"},

		// Line breaks become spaces so a two-line name reads as two words
		// rather than one fused one.
		{"newline becomes a space", "one\ntwo", "one two"},
		{"CR becomes a space", "one\rtwo", "one two"},
		{"tab becomes a space", "one\ttwo", "one two"},

		{"C0 controls go", "a\x00\x01\x07b", "ab"},
		{"DEL goes", "a\x7fb", "ab"},
		{"C1 controls go", "a\u0085\u009bb", "ab"},

		// The classic filename spoof: RLO makes "exe" render as the tail, so
		// the extension the user agrees to is not the one they get.
		{"RTL override", "report\u202egnp.exe", "reportgnp.exe"},
		{"bidi isolates", "a\u2066b\u2069c", "abc"},
		{"bidi marks", "a\u200eb\u200fc", "abc"},

		{"surrounding space is trimmed", "  spaced.pdf  ", "spaced.pdf"},
		{"a label of pure escapes empties", "\x1b[31m\x1b[0m", ""},
		{"invalid utf-8 is dropped", "a\xffb", "ab"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := SanitizeLabel(tc.in); got != tc.want {
				t.Errorf("SanitizeLabel(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// A sanitized label must never be able to disable the width clamp. truncateToWidth
// returns the row untouched when it sees an inline-image escape, so a name
// carrying that prefix would otherwise buy itself an unbounded row.
func TestSanitizeLabelStripsTheImageEscapesThatDisableTruncation(t *testing.T) {
	for _, in := range []string{"a\x1b]1337;File=inline=1:AAAA\x07b", "a\x1b_Gf=100\x1b\\b"} {
		got := SanitizeLabel(in)
		if containsImageEscape(got) {
			t.Errorf("SanitizeLabel(%q) = %q, which still disables truncation", in, got)
		}
	}
}

// cardFor renders one shared-file card and returns the raw rows, escapes and all.
func cardFor(t *testing.T, f SharedFile) string {
	t.Helper()
	raw, err := json.Marshal([]SharedFile{f})
	if err != nil {
		t.Fatal(err)
	}
	v := View{Theme: Dark, Now: func() time.Time { return pinnedNow }, Messages: []provider.Message{
		toolCall("t1", "share_file", `{"path":"x"}`),
		{
			Role:    provider.RoleTool,
			Content: []provider.Content{toolResult("t1", false, "Shared")},
			Meta:    map[string]string{MetaSharedKey: string(raw)},
		},
	}}
	return strings.Join(v.Build(80), "\n")
}

// A hostile name must contribute no escape bytes of its own to the card. The
// benign render is the control: the theme's own colouring is present in both,
// so any surplus came from the name.
func TestSharedFileCardNeutralisesAHostileName(t *testing.T) {
	benign := cardFor(t, SharedFile{ID: "shr_a", CallID: "t1", Name: "report.pdf", Kind: "document", Size: 2048})
	hostile := cardFor(t, SharedFile{
		ID: "shr_a", CallID: "t1", Kind: "document", Size: 2048,
		Name: "report\x1b[31m\x1b[2J\x1b]0;pwned\x07.pdf",
	})

	if got, want := strings.Count(hostile, "\x1b"), strings.Count(benign, "\x1b"); got != want {
		t.Errorf("the name contributed %d escape bytes to the card", got-want)
	}
	if strings.Contains(hostile, "pwned") {
		t.Errorf("the OSC payload reached the card:\n%s", hostile)
	}
	if !strings.Contains(stripANSI(hostile), "report.pdf") {
		t.Errorf("the readable part of the name should survive:\n%s", stripANSI(hostile))
	}
}

// The caption is the field the model writes freely — share_file hands the tool
// argument through untouched — so it is the one an injected prompt reaches.
func TestSharedFileCardNeutralisesAHostileCaption(t *testing.T) {
	benign := cardFor(t, SharedFile{
		ID: "shr_a", CallID: "t1", Name: "report.pdf", Kind: "document", Size: 2048,
		Caption: "the quarterly report",
	})
	hostile := cardFor(t, SharedFile{
		ID: "shr_a", CallID: "t1", Name: "report.pdf", Kind: "document", Size: 2048,
		Caption: "safe\x1b[31m\x1b[2Jtext\x1b]0;pwned\x07",
	})

	if got, want := strings.Count(hostile, "\x1b"), strings.Count(benign, "\x1b"); got != want {
		t.Errorf("the caption contributed %d escape bytes to the card", got-want)
	}
	if strings.Contains(hostile, "pwned") {
		t.Errorf("the OSC payload reached the card:\n%s", hostile)
	}
	if !strings.Contains(stripANSI(hostile), "safetext") {
		t.Errorf("the readable part of the caption should survive:\n%s", stripANSI(hostile))
	}
}

// A caption that is nothing but escapes has nothing to say, so it must not
// draw an empty indented line under the card.
func TestACaptionOfPureEscapesDrawsNoLine(t *testing.T) {
	none := cardFor(t, SharedFile{ID: "shr_a", CallID: "t1", Name: "report.pdf", Kind: "document", Size: 2048})
	escapes := cardFor(t, SharedFile{
		ID: "shr_a", CallID: "t1", Name: "report.pdf", Kind: "document", Size: 2048,
		Caption: "\x1b[31m\x1b[0m",
	})

	if got, want := len(strings.Split(escapes, "\n")), len(strings.Split(none, "\n")); got != want {
		t.Errorf("an all-escape caption drew %d extra line(s)", got-want)
	}
}

// A name that sanitizes to nothing must fall back, not leave the card nameless.
func TestANameOfPureEscapesFallsBack(t *testing.T) {
	out := stripANSI(cardFor(t, SharedFile{
		ID: "shr_a", CallID: "t1", Kind: "document", Size: 2048,
		Name: "\x1b[31m\x1b[0m",
	}))
	if !strings.Contains(out, "(unnamed)") {
		t.Errorf("a name that sanitizes away should read as unnamed:\n%s", out)
	}
}
