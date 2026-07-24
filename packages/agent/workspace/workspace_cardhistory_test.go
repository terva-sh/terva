package workspace

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/testsupport"
)

func cardHistoryWorkspace(t *testing.T) (*Workspace, context.Context) {
	t.Helper()
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	t.Setenv("OPENAI_API_KEY", "test-key")
	w, err := NewWorkspace(build.Args{Provider: "openai", Model: "gpt-5", CWD: testsupport.TempDir(t)}, "test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { w.Close() })
	return w, context.Background()
}

func historyCard(name, greeting string) string {
	return fmt.Sprintf(`{"spec":"chara_card_v2","spec_version":"2.0","data":{"name":%q,"first_mes":%q}}`, name, greeting)
}

func TestWorkspaceCardHistoryAndRestore(t *testing.T) {
	w, ctx := cardHistoryWorkspace(t)

	imported, err := w.CardsImport(ctx, ctrlproto.CardImportParams{Bytes: []byte(historyCard("Iris", "as imported"))})
	if err != nil {
		t.Fatal(err)
	}
	id := imported.ID

	// A card nobody has edited has an empty history — a normal state, not an error.
	h, err := w.CardsHistory(ctx, ctrlproto.CardHistoryParams{ID: id})
	if err != nil {
		t.Fatal(err)
	}
	if len(h.Revisions) != 0 {
		t.Fatalf("unedited card has no revisions, got %d", len(h.Revisions))
	}

	if _, err := w.CardsEdit(ctx, ctrlproto.CardEditParams{ID: id, Card: []byte(historyCard("Iris", "rewritten"))}); err != nil {
		t.Fatal(err)
	}
	h, err = w.CardsHistory(ctx, ctrlproto.CardHistoryParams{ID: id})
	if err != nil {
		t.Fatal(err)
	}
	if len(h.Revisions) != 1 {
		t.Fatalf("one edit, one revision, got %d", len(h.Revisions))
	}
	rev := h.Revisions[0]
	if rev.Ref == "" || rev.Saved.IsZero() || rev.Bytes == 0 || rev.Name != "Iris" {
		t.Fatalf("revision is missing metadata a list needs: %+v", rev)
	}

	// Restore returns the restored card, and the current card really moved back.
	restored, err := w.CardsRestore(ctx, ctrlproto.CardRestoreParams{ID: id, Ref: rev.Ref})
	if err != nil {
		t.Fatal(err)
	}
	if string(restored.Raw) != string(imported.Raw) {
		t.Errorf("restore did not round-trip\n got %s\nwant %s", restored.Raw, imported.Raw)
	}
	got, err := w.CardsGet(ctx, ctrlproto.CardGetParams{ID: id})
	if err != nil {
		t.Fatal(err)
	}
	if string(got.Raw) != string(imported.Raw) {
		t.Errorf("the stored card did not move back\n got %s\nwant %s", got.Raw, imported.Raw)
	}

	// …and the restore recorded what it replaced, so it can be undone in turn.
	h, err = w.CardsHistory(ctx, ctrlproto.CardHistoryParams{ID: id})
	if err != nil {
		t.Fatal(err)
	}
	if len(h.Revisions) != 2 {
		t.Fatalf("a restore is itself recorded, want 2 revisions, got %d", len(h.Revisions))
	}
	undone, err := w.CardsRestore(ctx, ctrlproto.CardRestoreParams{ID: id, Ref: h.Revisions[0].Ref})
	if err != nil {
		t.Fatal(err)
	}
	if undone.Raw == nil || string(undone.Raw) == string(imported.Raw) {
		t.Error("undoing the restore should bring the edited card back")
	}
}

// Revisions are newest first — the order a history list renders in.
func TestWorkspaceCardHistoryIsNewestFirst(t *testing.T) {
	w, ctx := cardHistoryWorkspace(t)

	imported, err := w.CardsImport(ctx, ctrlproto.CardImportParams{Bytes: []byte(historyCard("Iris", "v0"))})
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 3; i++ {
		if _, err := w.CardsEdit(ctx, ctrlproto.CardEditParams{
			ID: imported.ID, Card: []byte(historyCard("Iris", fmt.Sprintf("v%d", i))),
		}); err != nil {
			t.Fatal(err)
		}
	}
	h, err := w.CardsHistory(ctx, ctrlproto.CardHistoryParams{ID: imported.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(h.Revisions) != 3 {
		t.Fatalf("want 3 revisions, got %d", len(h.Revisions))
	}
	for i := 1; i < len(h.Revisions); i++ {
		if !h.Revisions[i-1].Saved.After(h.Revisions[i].Saved) {
			t.Fatalf("revisions must be newest first: %v then %v", h.Revisions[i-1].Saved, h.Revisions[i].Saved)
		}
	}
	// The newest revision is the state the LAST edit superseded (v2), not v0.
	raw, err := w.cardHistory().Get(imported.ID, h.Revisions[0].Ref)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(raw); !strings.Contains(got, `"v2"`) {
		t.Errorf("newest revision should be the last superseded card, got %s", got)
	}
}

func TestWorkspaceCardHistoryErrors(t *testing.T) {
	w, ctx := cardHistoryWorkspace(t)

	imported, err := w.CardsImport(ctx, ctrlproto.CardImportParams{Bytes: []byte(historyCard("Iris", "one"))})
	if err != nil {
		t.Fatal(err)
	}

	// An unknown card is "not found", distinct from a known card with no history.
	if _, err := w.CardsHistory(ctx, ctrlproto.CardHistoryParams{ID: "nobody-000000000000"}); err == nil {
		t.Error("history of an unknown card must fail")
	} else if !isNotFound(err) {
		t.Errorf("want not-found, got %v", err)
	}
	// An unknown or hostile revision ref is refused rather than reaching the disk.
	for _, ref := range []string{"", "9999999999999", "../../card", "abc"} {
		if _, err := w.CardsRestore(ctx, ctrlproto.CardRestoreParams{ID: imported.ID, Ref: ref}); err == nil {
			t.Errorf("restore of ref %q must fail", ref)
		} else if !isNotFound(err) {
			t.Errorf("ref %q: want not-found, got %v", ref, err)
		}
	}
}

func isNotFound(err error) bool {
	var ce *ctrlproto.Error
	return errors.As(err, &ce) && ce.Code == ctrlproto.CodeNotFound
}

// The list carries WHICH fields each revision differs in, so a row can say what
// restoring it would change without the client fetching every document.
func TestWorkspaceCardHistoryNamesChangedFields(t *testing.T) {
	w, ctx := cardHistoryWorkspace(t)

	src := `{"spec":"chara_card_v2","spec_version":"2.0","data":{"name":"Iris","first_mes":"hi","personality":"warm"}}`
	imported, err := w.CardsImport(ctx, ctrlproto.CardImportParams{Bytes: []byte(src)})
	if err != nil {
		t.Fatal(err)
	}
	// One edit that moves two fields.
	edited := `{"spec":"chara_card_v2","spec_version":"2.0","data":{"name":"Iris","first_mes":"hello","personality":"cold"}}`
	if _, err := w.CardsEdit(ctx, ctrlproto.CardEditParams{ID: imported.ID, Card: []byte(edited)}); err != nil {
		t.Fatal(err)
	}
	h, err := w.CardsHistory(ctx, ctrlproto.CardHistoryParams{ID: imported.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(h.Revisions) != 1 {
		t.Fatalf("want 1 revision, got %d", len(h.Revisions))
	}
	if got := h.Revisions[0].Fields; !reflect.DeepEqual(got, []string{"personality", "first_mes"}) {
		t.Errorf("fields = %v, want [personality first_mes]", got)
	}

	// Fields are measured against the card AS IT STANDS, not against the next
	// revision: restore the revision and it now differs in nothing.
	if _, err := w.CardsRestore(ctx, ctrlproto.CardRestoreParams{ID: imported.ID, Ref: h.Revisions[0].Ref}); err != nil {
		t.Fatal(err)
	}
	h, err = w.CardsHistory(ctx, ctrlproto.CardHistoryParams{ID: imported.ID})
	if err != nil {
		t.Fatal(err)
	}
	var matching *ctrlproto.CardRevision
	for i := range h.Revisions {
		if h.Revisions[i].Ref == "" {
			continue
		}
		if len(h.Revisions[i].Fields) == 0 {
			matching = &h.Revisions[i]
		}
	}
	if matching == nil {
		t.Fatalf("after restoring, one revision must match the card exactly: %+v", h.Revisions)
	}
}

func TestWorkspaceCardRevisionReadsOneInFull(t *testing.T) {
	w, ctx := cardHistoryWorkspace(t)

	imported, err := w.CardsImport(ctx, ctrlproto.CardImportParams{Bytes: []byte(historyCard("Iris", "as imported"))})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.CardsEdit(ctx, ctrlproto.CardEditParams{ID: imported.ID, Card: []byte(historyCard("Iris", "rewritten"))}); err != nil {
		t.Fatal(err)
	}
	h, err := w.CardsHistory(ctx, ctrlproto.CardHistoryParams{ID: imported.ID})
	if err != nil {
		t.Fatal(err)
	}
	ref := h.Revisions[0].Ref

	rev, err := w.CardsRevision(ctx, ctrlproto.CardRevisionParams{ID: imported.ID, Ref: ref})
	if err != nil {
		t.Fatal(err)
	}
	if rev.Ref != ref || rev.Name != "Iris" || rev.Saved.IsZero() {
		t.Errorf("revision metadata is wrong: %+v", rev)
	}
	if string(rev.Raw) != string(imported.Raw) {
		t.Errorf("raw is the stored revision\n got %s\nwant %s", rev.Raw, imported.Raw)
	}
	if !reflect.DeepEqual(rev.Fields, []string{"first_mes"}) {
		t.Errorf("fields = %v, want [first_mes]", rev.Fields)
	}

	// Reading a revision changes nothing — this is the read side of the split.
	after, err := w.CardsGet(ctx, ctrlproto.CardGetParams{ID: imported.ID})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(after.Raw), "rewritten") {
		t.Error("cards.revision must not write the card back")
	}
	if h2, _ := w.CardsHistory(ctx, ctrlproto.CardHistoryParams{ID: imported.ID}); len(h2.Revisions) != 1 {
		t.Errorf("reading a revision must not record one, got %d", len(h2.Revisions))
	}

	// Unknown card and hostile ref are refused the same way restore refuses them.
	if _, err := w.CardsRevision(ctx, ctrlproto.CardRevisionParams{ID: "nobody-000000000000", Ref: ref}); !isNotFound(err) {
		t.Errorf("unknown card: want not-found, got %v", err)
	}
	for _, bad := range []string{"", "../../card", "abc", "9999999999999"} {
		if _, err := w.CardsRevision(ctx, ctrlproto.CardRevisionParams{ID: imported.ID, Ref: bad}); !isNotFound(err) {
			t.Errorf("ref %q: want not-found, got %v", bad, err)
		}
	}
}

// An import that lands on a card you already have goes through the same
// recorded write as an edit, so the wire sees it as an ordinary revision — and
// the import itself says what it displaced.
func TestWorkspaceReimportIsRecordedAndAnnounced(t *testing.T) {
	w, ctx := cardHistoryWorkspace(t)

	src := `{"spec":"chara_card_v2","spec_version":"2.0","data":{"name":"Iris","first_mes":"as imported"}}`
	imported, err := w.CardsImport(ctx, ctrlproto.CardImportParams{Bytes: []byte(src)})
	if err != nil {
		t.Fatal(err)
	}
	if len(imported.Warnings) != 0 {
		t.Errorf("a fresh import announces no replacement: %v", imported.Warnings)
	}
	if _, err := w.CardsEdit(ctx, ctrlproto.CardEditParams{ID: imported.ID, Card: []byte(historyCard("Iris", "MY EDIT"))}); err != nil {
		t.Fatal(err)
	}

	again, err := w.CardsImport(ctx, ctrlproto.CardImportParams{Bytes: []byte(src)})
	if err != nil {
		t.Fatal(err)
	}
	if again.ID != imported.ID {
		t.Fatalf("re-import should land on the same card")
	}
	if !strings.Contains(strings.Join(again.Warnings, " "), "already in your library") {
		t.Errorf("the reverting import must say so on the wire: %v", again.Warnings)
	}
	h, err := w.CardsHistory(ctx, ctrlproto.CardHistoryParams{ID: imported.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(h.Revisions) != 2 {
		t.Fatalf("the displaced edit must be a revision, got %d", len(h.Revisions))
	}
	// And it is recoverable through the ordinary restore.
	back, err := w.CardsRestore(ctx, ctrlproto.CardRestoreParams{ID: imported.ID, Ref: h.Revisions[0].Ref})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(back.Raw), "MY EDIT") {
		t.Error("restoring must bring the displaced edit back")
	}
	// A data-only history reports no portrait movement.
	for _, r := range h.Revisions {
		if r.Portrait {
			t.Errorf("no picture was ever involved: %+v", r)
		}
	}
}
