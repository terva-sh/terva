package tools

import (
	"strings"
	"testing"

	"terva.sh/terva/packages/core"
)

// The credential used throughout. It carries a leading space because the
// redaction rule anchors with `\b` — glued to a run of letters there is no word
// boundary and the rule never matches, which would make every assertion below
// pass for the wrong reason.
const (
	ssKeyPrefix = "sk-ant-api03-"
	ssKeyTail   = "TAILSECRETXYZ"
	ssKey       = ssKeyPrefix + "XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX" + ssKeyTail
)

// The premise every test here rests on: this fixture really is a credential by
// the redactor's own definition. A fixture the rule does not recognise would
// make "no leak" mean "nothing to leak".
func TestRedactionFixtureIsActuallyACredential(t *testing.T) {
	if got := core.RedactSecrets(" " + ssKey + " "); strings.Contains(got, ssKeyTail) {
		t.Fatalf("the test fixture is not recognised as a secret, so these tests would prove nothing: %q", got)
	}
}

// ssSnippet used to redact only a window around its match, widened by 64 bytes.
// The rules anchor on a credential's PREFIX, so a key whose prefix fell outside
// that window matched nothing and its TAIL came back verbatim — returned to the
// model and written into the calling session's transcript. session_search scans
// every session in a project, so a key pasted once could resurface from any
// later session.
//
// The layout is arithmetic, not guesswork. ssSnippetMax is 200 and the window
// starts at match-66, so placing the key at 500 (length 76, ending 576) and the
// needle at 600 puts the window start at 534 — INSIDE the key. The prefix at 500
// is outside; the tail is inside. That is precisely the gap that leaked.
func TestSnippetDoesNotLeakAKeyStraddlingTheWindowEdge(t *testing.T) {
	text := strings.Repeat("a", 499) + " " + ssKey + " " + strings.Repeat("b", 22) + "needle" + strings.Repeat("c", 200)

	// Confirm the geometry actually reproduces the straddle, or the test is
	// asserting against a layout it did not create.
	keyAt := strings.Index(text, ssKeyPrefix)
	needleAt := strings.Index(text, "needle")
	windowStart := needleAt - ssSnippetMax/3
	if !(keyAt < windowStart && windowStart < keyAt+len(ssKey)) {
		t.Fatalf("layout does not straddle the window edge: key at %d (len %d), window starts at %d",
			keyAt, len(ssKey), windowStart)
	}

	got := ssSnippet(text, "needle")
	if strings.Contains(got, ssKeyTail) {
		t.Errorf("leaked the tail of a credential whose prefix sat outside the window:\n  %q", got)
	}
	if !strings.Contains(got, "needle") {
		t.Errorf("snippet lost the thing that was searched for: %q", got)
	}
}

// A key sitting directly on the match still has to be masked — the case the old
// code did handle, kept so the fix cannot be read as narrowing coverage.
func TestSnippetRedactsAKeyBesideTheMatch(t *testing.T) {
	text := "before needle " + ssKey + " after"
	got := ssSnippet(text, "needle")
	if strings.Contains(got, ssKeyTail) || strings.Contains(got, ssKeyPrefix+"X") {
		t.Errorf("leaked a key adjacent to the match: %q", got)
	}
}

// The window must cut on rune boundaries. The old code sliced with a bare
// `red[:ssSnippetMax]`, which could split a multi-byte rune and emit invalid
// UTF-8 into a tool result.
func TestSnippetCutsOnARuneBoundary(t *testing.T) {
	text := "needle " + strings.Repeat("é", 400)
	got := ssSnippet(text, "needle")
	if strings.ContainsRune(got, '�') {
		t.Fatalf("snippet contains an invalid rune: %q", got)
	}
	// The ellipsis the renderer adds is legitimate; everything else must be
	// valid UTF-8 that round-trips.
	if !strings.HasPrefix(got, "needle") {
		t.Fatalf("snippet did not start at the match: %q", got)
	}
}

// A search term that is itself part of a credential must not steer the window
// onto the secret: the needle is located in the REDACTED text, so a term that
// was part of a key correctly stops matching.
func TestSnippetDoesNotAimTheWindowUsingACredential(t *testing.T) {
	text := strings.Repeat("a", 299) + " " + ssKey + " " + strings.Repeat("b", 300)
	got := ssSnippet(text, strings.ToLower(ssKeyTail))
	if strings.Contains(got, ssKeyTail) {
		t.Errorf("searching for a fragment of a key returned the key: %q", got)
	}
}
