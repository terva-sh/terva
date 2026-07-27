package dialogs

import (
	"testing"
	"time"

	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/tui"
)

func answer(t *testing.T, resp chan core.ConfirmDecision) core.ConfirmDecision {
	t.Helper()
	select {
	case d := <-resp:
		return d
	case <-time.After(2 * time.Second):
		t.Fatal("no decision delivered")
		return core.ConfirmDecision{}
	}
}

// A request with derived grant scopes gains a sixth option that persists
// the scoped patterns; the five fixed options keep their numbers so a
// habitual "5 = no" can never land on a grant.
func TestConfirmDialogScopedOption(t *testing.T) {
	d := NewConfirmDialog()
	resp := make(chan core.ConfirmDecision, 1)
	d.Enqueue(&ConfirmRequest{
		ToolName: "bash",
		Preview:  "git status",
		Scopes:   []core.GrantScope{{Display: "git status", Pattern: `^git status(?:\s|$)`}},
		Resp:     resp,
	})

	if !d.HandleKey(tui.Key{Kind: tui.KeyRune, Rune: '6'}) {
		t.Fatal("'6' should resolve a scoped request")
	}
	got := answer(t, resp)
	if !got.Allow || !got.PersistTool {
		t.Fatalf("scoped option = %+v, want allow+persist", got)
	}
	if len(got.PersistScopes) != 1 || got.PersistScopes[0] != `^git status(?:\s|$)` {
		t.Fatalf("PersistScopes = %v, want the offered pattern echoed back", got.PersistScopes)
	}
}

// '5' stays "no" on a scoped request, and '6' is dead on an unscoped one.
func TestConfirmDialogNumberingStable(t *testing.T) {
	d := NewConfirmDialog()
	resp := make(chan core.ConfirmDecision, 1)
	d.Enqueue(&ConfirmRequest{
		ToolName: "bash",
		Preview:  "git status",
		Scopes:   []core.GrantScope{{Display: "git status", Pattern: `^git status(?:\s|$)`}},
		Resp:     resp,
	})
	if !d.HandleKey(tui.Key{Kind: tui.KeyRune, Rune: '5'}) {
		t.Fatal("'5' should resolve")
	}
	if got := answer(t, resp); got.Allow {
		t.Fatalf("'5' = %+v, want the refusal it has always been", got)
	}

	resp2 := make(chan core.ConfirmDecision, 1)
	d.Enqueue(&ConfirmRequest{ToolName: "bash", Preview: "ls", Resp: resp2})
	if d.HandleKey(tui.Key{Kind: tui.KeyRune, Rune: '6'}) {
		t.Fatal("'6' must be inert when no scopes were derived")
	}
	if !d.HandleKey(tui.Key{Kind: tui.KeyRune, Rune: '1'}) {
		t.Fatal("'1' should still resolve the unscoped request")
	}
	if got := answer(t, resp2); !got.Allow || got.PersistTool || len(got.PersistScopes) != 0 {
		t.Fatalf("'1' = %+v, want plain allow-once", got)
	}
}

// Enter on a cursor parked past the end of a shorter option list (scoped
// request answered, unscoped one behind it) must clamp, not panic.
func TestConfirmDialogCursorClampsAcrossRequests(t *testing.T) {
	d := NewConfirmDialog()
	respA := make(chan core.ConfirmDecision, 1)
	respB := make(chan core.ConfirmDecision, 1)
	d.Enqueue(&ConfirmRequest{
		ToolName: "bash", Preview: "git status",
		Scopes: []core.GrantScope{{Display: "git status", Pattern: `^git status(?:\s|$)`}},
		Resp:   respA,
	})
	d.Enqueue(&ConfirmRequest{ToolName: "bash", Preview: "ls", Resp: respB})

	for range 5 {
		d.HandleKey(tui.Key{Kind: tui.KeyDown})
	}
	if !d.HandleKey(tui.Key{Kind: tui.KeyEnter}) {
		t.Fatal("enter should resolve the scoped head")
	}
	if got := answer(t, respA); len(got.PersistScopes) != 1 {
		t.Fatalf("cursor at 6th option = %+v, want the scoped grant", got)
	}
	// Head is now the unscoped request; cursor was reset on dequeue, but
	// hammer down past its end and answer to prove the clamp holds.
	for range 9 {
		d.HandleKey(tui.Key{Kind: tui.KeyDown})
	}
	if !d.HandleKey(tui.Key{Kind: tui.KeyEnter}) {
		t.Fatal("enter should resolve the unscoped head")
	}
	if got := answer(t, respB); got.Allow {
		t.Fatalf("last option = %+v, want the refusal (option 5)", got)
	}
}
