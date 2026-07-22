package workspace

import (
	"context"
	"testing"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/testsupport"
)

// TestWorkspaceSessionGroups drives the sessiongroups.* verbs through a real
// Workspace: create → assign members → save-preserves-members → stale filtering
// on a deleted session → delete. The membership never touches the sessions.
func TestWorkspaceSessionGroups(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	t.Setenv("OPENAI_API_KEY", "test-key")
	w, err := NewWorkspace(build.Args{Provider: "openai", Model: "gpt-5", CWD: testsupport.TempDir(t)}, "test")
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	ctx := context.Background()

	// Two real sessions to file.
	a, err := w.CreateSession(ctx, ctrlproto.CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}
	b, err := w.CreateSession(ctx, ctrlproto.CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}

	if r, err := w.SessionGroupsList(ctx); err != nil || len(r.Groups) != 0 {
		t.Fatalf("empty groups: %v, %d", err, len(r.Groups))
	}

	g, err := w.SessionGroupSave(ctx, ctrlproto.SessionGroupSaveParams{Name: "Playtests", Color: "#61afef"})
	if err != nil {
		t.Fatal(err)
	}
	if g.ID == "" || g.Name != "Playtests" || len(g.Members) != 0 {
		t.Fatalf("create returned %+v", g)
	}
	if _, err := w.SessionGroupSave(ctx, ctrlproto.SessionGroupSaveParams{Name: " "}); err == nil {
		t.Error("a blank name should be refused")
	}

	// Assign both sessions plus a bogus id that must be dropped.
	set, err := w.SessionGroupSetMembers(ctx, ctrlproto.SessionGroupSetMembersParams{ID: g.ID, Members: []string{a.ID, "ghost-session", b.ID}})
	if err != nil {
		t.Fatal(err)
	}
	if len(set.Members) != 2 {
		t.Fatalf("phantom id not dropped: %v", set.Members)
	}

	// A metadata-only save (recolour) keeps the membership.
	saved, err := w.SessionGroupSave(ctx, ctrlproto.SessionGroupSaveParams{ID: g.ID, Name: "Playtests", Color: "#98c379"})
	if err != nil {
		t.Fatal(err)
	}
	if saved.Color != "#98c379" || len(saved.Members) != 2 {
		t.Fatalf("save clobbered members: %+v", saved)
	}

	// Delete a member session — the listing drops the stale id on read.
	if err := w.DeleteSession(ctx, b.ID); err != nil {
		t.Fatal(err)
	}
	list, err := w.SessionGroupsList(ctx)
	if err != nil || len(list.Groups) != 1 {
		t.Fatalf("list: %v, %+v", err, list)
	}
	if got := list.Groups[0].Members; len(got) != 1 || got[0] != a.ID {
		t.Fatalf("stale member not filtered: %v", got)
	}

	if err := w.SessionGroupDelete(ctx, ctrlproto.SessionGroupDeleteParams{ID: g.ID}); err != nil {
		t.Fatal(err)
	}
	if r, err := w.SessionGroupsList(ctx); err != nil || len(r.Groups) != 0 {
		t.Fatalf("group not deleted: %v, %d", err, len(r.Groups))
	}
	if err := w.SessionGroupDelete(ctx, ctrlproto.SessionGroupDeleteParams{ID: g.ID}); err == nil {
		t.Error("deleting a missing group should error")
	}
}
