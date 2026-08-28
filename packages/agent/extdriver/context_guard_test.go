package extdriver

import (
	"strings"
	"testing"
)

// Extension cards ride the ephemeral tail as a user-role message — the position
// that won 20 of 20 final answers away from the user on the inactive-groups
// note. The section therefore leads with the prohibition.
//
// Once for the SECTION, not once per card: the wrapper already attributes each
// card to its extension, and repeating the sentence per card would spend budget
// telling the model the same thing five times.
func TestEphemeralContextLeadsWithOneBackgroundGuard(t *testing.T) {
	e := &Extension{Manifest: Manifest{Name: "alpha"}}
	e.contextCards = map[string]contextCard{
		"one": {text: "task board: 3 open"},
		"two": {text: "build: green"},
	}
	d := &Driver{ext: map[string]*Extension{"alpha": e}}

	out := d.EphemeralContext()
	if !strings.Contains(out, "task board") || !strings.Contains(out, "build: green") {
		t.Fatalf("precondition: cards missing from the section:\n%s", out)
	}
	if !strings.HasPrefix(out, "[background] Do not reply") {
		t.Errorf("the section does not LEAD with the prohibition:\n%s", out)
	}
	if n := strings.Count(out, "[background]"); n != 1 {
		t.Errorf("want exactly 1 guard for the section, got %d (once per card?):\n%s", n, out)
	}
	if strings.Index(out, "[background]") > strings.Index(out, "<extension-context") {
		t.Errorf("a card precedes the prohibition; prohibition-first is the measured ordering:\n%s", out)
	}
}

// The guard must not conjure a section out of nothing. Callers test this for ""
// to decide whether extension context exists at all, so a lone guard
// introducing no cards would both lie and cost tokens every turn of every
// session that has no context-contributing extension — which is most of them.
func TestEphemeralContextStaysEmptyWithNoCards(t *testing.T) {
	d := &Driver{ext: map[string]*Extension{
		"alpha": {Manifest: Manifest{Name: "alpha"}},
	}}
	if out := d.EphemeralContext(); out != "" {
		t.Errorf("no cards must yield an EMPTY section, got:\n%s", out)
	}
}
