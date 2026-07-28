package slug

import (
	"strings"
	"testing"
)

// The fold exists for one load-bearing reason: persona shadowing is by file
// stem, and the built-in crew's stems are folded spellings (seppa.md for
// Seppä). A stem that drops the letter instead of folding it mints a name no
// built-in uses, so a copy-to-edit silently never shadows — and the card
// doctor, which resolves its persona by the literal stem "seppa", never sees
// the edit. build/cardslug_fold_test.go holds that end-to-end behaviour; this
// pins the string handling underneath it.
func TestOfFoldsDiacritics(t *testing.T) {
	cases := []struct{ name, want string }{
		{"Seppä", "seppa"},
		{"Zoë", "zoe"},
		{"Café au Lait", "cafe-au-lait"},
		{"Custom One", "custom-one"},
		{"!!!", ""},
	}
	for _, c := range cases {
		if got := Of(c.name); got != c.want {
			t.Errorf("Of(%q) = %q, want %q", c.name, got, c.want)
		}
	}
	// The legacy slugger is pinned too: stores probe it to find files minted
	// before the fold, so its behaviour must stay exactly what shipped.
	if got := Legacy("Seppä"); got != "sepp" {
		t.Errorf("Legacy(Seppä) = %q, want sepp", got)
	}
	if got := Legacy("Zoë"); got != "zo" {
		t.Errorf("Legacy(Zoë) = %q, want zo", got)
	}
	// For pure-ASCII names the two agree — that equality is the cheap
	// "no pre-fold file can exist" test every fallback site relies on.
	if Legacy("Custom One") != Of("Custom One") {
		t.Error("Legacy should equal Of for ASCII names")
	}
}

// A stem is capped, and the cap must not leave a trailing dash behind.
func TestOfCapsLength(t *testing.T) {
	got := Of(strings.Repeat("a", 100))
	if len(got) != 32 {
		t.Errorf("Of(100 a's) = %q (len %d), want 32", got, len(got))
	}
	if got := Of("a " + strings.Repeat("b", 60)); strings.HasSuffix(got, "-") {
		t.Errorf("Of(...) = %q, want no trailing dash after the cap", got)
	}
}

// ID is the stem plus a content hash, and an empty stem must still produce a
// usable id rather than a leading dash.
func TestIDShape(t *testing.T) {
	id := ID("Zoë", []byte("content"))
	if !strings.HasPrefix(id, "zoe-") || len(id) != len("zoe-")+12 {
		t.Errorf("ID(Zoë) = %q, want zoe- plus a 12-char hash", id)
	}
	if same := ID("Zoë", []byte("content")); same != id {
		t.Errorf("ID is not deterministic: %q then %q", id, same)
	}
	if other := ID("Zoë", []byte("different")); other == id {
		t.Error("ID ignored the content: two bodies produced the same id")
	}
	if bare := ID("!!!", []byte("content")); strings.HasPrefix(bare, "-") || len(bare) != 12 {
		t.Errorf("ID with an empty stem = %q, want the bare 12-char hash", bare)
	}
}

// The ids these guard arrive off the wire, so the traversal cases are the point.
func TestValidIDRejectsTraversal(t *testing.T) {
	for _, bad := range []string{"", "..", "../etc", "a/b", `a\b`, "x/../y"} {
		if err := ValidID(bad); err == nil {
			t.Errorf("ValidID(%q) = nil, want an error — this id can escape the library dir", bad)
		}
	}
	for _, ok := range []string{"zoe-0123456789ab", "seppa", "a-b-c"} {
		if err := ValidID(ok); err != nil {
			t.Errorf("ValidID(%q) = %v, want nil", ok, err)
		}
	}
}
