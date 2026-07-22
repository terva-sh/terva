package workspace

import (
	"context"
	"testing"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/testsupport"
)

// TestWorkspaceCardGroups drives the cardgroups.* verbs through a real
// Workspace: create → assign members → save-preserves-members → stale filtering
// → delete. The group filing never touches the cards themselves.
func TestWorkspaceCardGroups(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	t.Setenv("OPENAI_API_KEY", "test-key")
	w, err := NewWorkspace(build.Args{Provider: "openai", Model: "gpt-5", CWD: testsupport.TempDir(t)}, "test")
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	ctx := context.Background()

	// Two real cards to file.
	a, err := w.CardsImport(ctx, ctrlproto.CardImportParams{Bytes: []byte(`{"name":"Alice","first_mes":"hi"}`)})
	if err != nil {
		t.Fatal(err)
	}
	b, err := w.CardsImport(ctx, ctrlproto.CardImportParams{Bytes: []byte(`{"name":"Bob","first_mes":"yo"}`)})
	if err != nil {
		t.Fatal(err)
	}

	if r, err := w.CardGroupsList(ctx); err != nil || len(r.Groups) != 0 {
		t.Fatalf("empty groups: %v, %d", err, len(r.Groups))
	}

	// Create a group (no id) — members start empty.
	wip, err := w.CardGroupSave(ctx, ctrlproto.CardGroupSaveParams{Name: "WIP", Color: "#c80"})
	if err != nil {
		t.Fatal(err)
	}
	if wip.ID == "" || wip.Name != "WIP" || wip.Color != "#c80" || len(wip.Members) != 0 {
		t.Fatalf("create returned %+v", wip)
	}
	if _, err := w.CardGroupSave(ctx, ctrlproto.CardGroupSaveParams{Name: "  "}); err == nil {
		t.Error("a blank name should be refused")
	}

	// Assign both cards, plus a bogus ref that must be dropped.
	set, err := w.CardGroupSetMembers(ctx, ctrlproto.CardGroupSetMembersParams{ID: wip.ID, Members: []string{a.ID, "ghost-card", b.ID}})
	if err != nil {
		t.Fatal(err)
	}
	if len(set.Members) != 2 {
		t.Fatalf("phantom ref not dropped: %v", set.Members)
	}

	// A metadata-only save (rename) must keep the membership.
	renamed, err := w.CardGroupSave(ctx, ctrlproto.CardGroupSaveParams{ID: wip.ID, Name: "Working"})
	if err != nil {
		t.Fatal(err)
	}
	if renamed.Name != "Working" || len(renamed.Members) != 2 {
		t.Fatalf("save clobbered members: %+v", renamed)
	}

	// Delete a member card — the group listing drops the stale ref on read,
	// without the delete having to touch the group.
	if err := w.CardsDelete(ctx, ctrlproto.CardDeleteParams{ID: b.ID}); err != nil {
		t.Fatal(err)
	}
	list, err := w.CardGroupsList(ctx)
	if err != nil || len(list.Groups) != 1 {
		t.Fatalf("list: %v, %+v", err, list)
	}
	if got := list.Groups[0].Members; len(got) != 1 || got[0] != a.ID {
		t.Fatalf("stale member not filtered: %v", got)
	}

	// Delete the group; the surviving card is untouched.
	if err := w.CardGroupDelete(ctx, ctrlproto.CardGroupDeleteParams{ID: wip.ID}); err != nil {
		t.Fatal(err)
	}
	if r, err := w.CardGroupsList(ctx); err != nil || len(r.Groups) != 0 {
		t.Fatalf("group not deleted: %v, %d", err, len(r.Groups))
	}
	if _, err := w.CardsGet(ctx, ctrlproto.CardGetParams{ID: a.ID}); err != nil {
		t.Errorf("deleting a group deleted its card: %v", err)
	}
	if err := w.CardGroupDelete(ctx, ctrlproto.CardGroupDeleteParams{ID: wip.ID}); err == nil {
		t.Error("deleting a missing group should error")
	}
}
