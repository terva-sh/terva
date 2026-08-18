package secretadmin

import (
	"bytes"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/ctrlproto"
)

func statusWith(store ctrlproto.SecretsStore, grants []ctrlproto.SecretsGrant) ctrlproto.SecretsStatus {
	return ctrlproto.SecretsStatus{Store: store, Grants: grants}
}

func render(t *testing.T, st ctrlproto.SecretsStatus) string {
	t.Helper()
	var b bytes.Buffer
	WriteStatus(&b, st)
	return b.String()
}

// The grants section was gated on st.Store.Present, and Present does not mean
// "the store file exists" — it means AT LEAST ONE SCOPE HOLDS A VALUE.
//
// So `terva secret grant` on a home that stores no values produced a grant that
// was in force, carried over the wire by secrets.status, and completely absent
// from the CLI. A permission you cannot see is the worst of the two states to
// be in: nothing tells you to revoke it.
func TestAGrantIsShownEvenWhenNoScopeHoldsAValue(t *testing.T) {
	out := render(t, statusWith(
		ctrlproto.SecretsStore{Present: false},
		[]ctrlproto.SecretsGrant{{Principal: "ext:indexer", Scope: "core:openai", Mode: "use"}},
	))

	if !strings.Contains(out, "ext:indexer") {
		t.Errorf("a live grant is missing from the status output entirely:\n%s", out)
	}
	if !strings.Contains(out, "core:openai") {
		t.Errorf("the granted scope is not shown:\n%s", out)
	}
}

// The complement: on a home with a store but no grants, the section must still
// render, because one that vanishes when empty reads as missing rather than as
// empty. This is the behaviour the original gate was written for and it must
// survive the fix.
func TestTheGrantsSectionStillRendersEmptyOnceAStoreExists(t *testing.T) {
	out := render(t, statusWith(ctrlproto.SecretsStore{Present: true}, nil))
	if !strings.Contains(out, "grants") {
		t.Errorf("the grants section vanished on a home with a store and no grants:\n%s", out)
	}
	if !strings.Contains(out, "none") {
		t.Errorf("an empty grants section must say so explicitly:\n%s", out)
	}
}

// And the other complement: a home with neither must NOT grow a grants row,
// which would restate the store line in different words. Without this,
// rendering unconditionally would pass both tests above.
func TestNoGrantsSectionOnAHomeWithNoStoreAndNoGrants(t *testing.T) {
	out := render(t, statusWith(ctrlproto.SecretsStore{Present: false}, nil))
	if strings.Contains(out, "grants") {
		t.Errorf("a home with no store and no grants grew a grants row:\n%s", out)
	}
}
