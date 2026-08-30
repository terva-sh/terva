package tools

import (
	"fmt"
	"strings"
	"testing"
)

func TestBareCarriageReturnsCounting(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want int
	}{
		{"none", "a\nb", 0},
		{"crlf is a line ending, not noise", "a\r\nb\r\nc", 0},
		{"one bare cr", "a\rb", 1},
		{"crlf and bare mixed", "a\r\nb\rc\r\nd\re", 2},
		{"trailing bare cr", "a\r", 1},
		{"classic mac: every cr is a line ending", "a\rb\rc", 2},
	}
	for _, c := range cases {
		if got := bareCarriageReturns(c.in); got != c.want {
			t.Errorf("%s: bareCarriageReturns(%q) = %d, want %d", c.name, c.in, got, c.want)
		}
	}
}

func TestStripBareCarriageReturnsKeepsLineEndings(t *testing.T) {
	cases := []struct{ in, want string }{
		{"a\nb", "a\nb"},
		{"a\r\nb", "a\r\nb"},     // a real line ending survives
		{"a\rb", "ab"},           // noise goes
		{"a\r\nb\rc", "a\r\nbc"}, // the mixed case: keep the pair, drop the stray
	}
	for _, c := range cases {
		if got := stripBareCarriageReturns(c.in); got != c.want {
			t.Errorf("stripBareCarriageReturns(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	if got := carriageReturnPhrase(1); !strings.Contains(got, "1 carriage return that is not") {
		t.Errorf("singular phrasing wrong: %q", got)
	}
	if got := carriageReturnPhrase(2); !strings.Contains(got, "2 carriage returns that are not") {
		t.Errorf("plural phrasing wrong: %q", got)
	}
}

// The recorded failure, reduced. The model emitted oldText with a \r before
// every token; normalizeNewlines turned each into a newline and the match
// missed against a file that was perfectly fine. The old message said the file
// was byte-identical and that nothing resembled the block — both true, neither
// actionable. The note has to name the invisible bytes AND prove the claim by
// pointing at the line the cleaned block really occupies.
func TestEditBareCarriageReturnNoteProvesTheLine(t *testing.T) {
	block := "          <g class=\"eyes-open\">\n            <circle class=\"eye\"/>\n          </g>"
	content := "<div>\n" + block + "\n</div>\n"

	// The recorded shape: a \r before every token boundary, mixed INTO text
	// that still carries its real \n. Generated from the block rather than
	// typed out, so the fixture cannot drift from the file by a miscount.
	noise := func(s string) string {
		lines := strings.Split(s, "\n")
		for i, ln := range lines {
			lines[i] = strings.ReplaceAll(ln, " ", "\r ")
		}
		return strings.Join(lines, "\n")
	}
	crOld := noise(block)
	// Sanity: stripping the noise must reproduce the file's block exactly,
	// otherwise this test is not exercising what it claims.
	if got := stripBareCarriageReturns(crOld); got != block {
		t.Fatalf("fixture is wrong: stripped oldText != file block\n got: %q\nwant: %q", got, block)
	}

	_, err := runEdit(t, content, map[string]any{"oldText": crOld, "newText": "REPLACED"})
	if err == nil {
		t.Fatal("expected the edit to fail: normalization shreds oldText before the matcher sees it")
	}
	msg := err.Error()

	n := bareCarriageReturns(crOld)
	if want := fmt.Sprintf("%d carriage returns that are not line endings", n); !strings.Contains(msg, want) {
		t.Errorf("error does not name the invisible bytes (%q):\n%s", want, msg)
	}
	// The block sits on line 2 of the file, so the note must say so rather
	// than telling the model to go looking.
	if !strings.Contains(msg, "matches at line 2") {
		t.Errorf("error does not PROVE the fix by naming the line:\n%s", msg)
	}
}

// When removing the CRs is not enough on its own, the note must still name
// them, but it must not claim a match it did not verify.
func TestEditBareCarriageReturnNoteWithoutProofMakesNoClaim(t *testing.T) {
	_, err := runEdit(t, "alpha\nbeta\n", map[string]any{
		"oldText": "\rnowhere\rnear\rthe\rfile",
		"newText": "x",
	})
	if err == nil {
		t.Fatal("expected the edit to fail")
	}
	msg := err.Error()
	if !strings.Contains(msg, "carriage returns that are not line endings") {
		t.Errorf("error should still name the CRs:\n%s", msg)
	}
	if strings.Contains(msg, "matches at line") {
		t.Errorf("error claims a match it never verified:\n%s", msg)
	}
	if !strings.Contains(msg, "remove them and retry") {
		t.Errorf("error should still say what to do:\n%s", msg)
	}
}

// An ordinary miss must not grow a carriage-return paragraph it has no
// evidence for.
func TestEditWithoutCarriageReturnsGetsNoNote(t *testing.T) {
	_, err := runEdit(t, "alpha\nbeta\n", map[string]any{
		"oldText": "gamma",
		"newText": "delta",
	})
	if err == nil {
		t.Fatal("expected the edit to fail")
	}
	if strings.Contains(err.Error(), "carriage return") {
		t.Errorf("clean oldText should produce no CR note:\n%s", err)
	}
}

// The load-bearing case, pinned so the diagnostic above can never tempt anyone
// into weakening it. Seven edits in the recorded session carried \r\n against
// LF files and SUCCEEDED only because normalizeNewlines collapses them.
func TestEditCRLFOldTextStillMatches(t *testing.T) {
	got, err := runEdit(t, "alpha\nbeta\ngamma\n", map[string]any{
		"oldText": "alpha\r\nbeta",
		"newText": "alpha\r\nBETA",
	})
	if err != nil {
		t.Fatalf("a CRLF oldText against an LF file must still match: %v", err)
	}
	if want := "alpha\nBETA\ngamma\n"; got != want {
		t.Errorf("content = %q, want %q", got, want)
	}
}

// "oldText equals newText" is nonsense to a model looking at two visibly
// different strings. Say which invisible difference collapsed.
func TestEditEqualsNewTextNamesNormalization(t *testing.T) {
	_, err := runEdit(t, "x\ny\n", map[string]any{
		"oldText": "x\r\ny",
		"newText": "x\ny",
	})
	if err == nil {
		t.Fatal("expected the edit to be refused")
	}
	if !strings.Contains(err.Error(), "after line-ending normalization") {
		t.Errorf("error does not explain why two different strings are equal:\n%s", err)
	}
}

// A genuinely identical pair keeps the plain message: there is no
// normalization story to tell, and inventing one would mislead.
func TestEditEqualsNewTextPlainStaysPlain(t *testing.T) {
	_, err := runEdit(t, "x\ny\n", map[string]any{
		"oldText": "x\ny",
		"newText": "x\ny",
	})
	if err == nil {
		t.Fatal("expected the edit to be refused")
	}
	if strings.Contains(err.Error(), "after line-ending normalization") {
		t.Errorf("identical strings must not blame normalization:\n%s", err)
	}
}
