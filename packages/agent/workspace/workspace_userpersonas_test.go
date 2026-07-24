package workspace

import (
	"context"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/ctrlproto"
)

// Saved user personas: a default pre-fills a fresh chat's identity (so the
// greeting's {{user}} resolves to the persona, not the literal "User"), and
// user.bind by ref applies a stored persona.
func TestUserPersonasDefaultAndRef(t *testing.T) {
	w := draftWorkspace(t)
	ctx := context.Background()

	if _, err := w.UserPersonaSave(ctx, ctrlproto.UserPersonaView{Name: "Kira", Description: "A wary courier."}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.UserPersonaSave(ctx, ctrlproto.UserPersonaView{Name: "Aria"}); err != nil {
		t.Fatal(err)
	}
	if list, _ := w.UserPersonasList(ctx); len(list.Personas) != 2 {
		t.Fatalf("list = %+v, want 2", list.Personas)
	}

	// The default pre-fills a fresh immersive session: greeting {{user}} -> Kira.
	if err := w.UserPersonaSetDefault(ctx, ctrlproto.UserPersonaRef{Ref: "kira"}); err != nil {
		t.Fatal(err)
	}
	imported, err := w.CardsImport(ctx, ctrlproto.CardImportParams{Bytes: []byte(`{"name":"Ivy","first_mes":"Hello {{user}}."}`)})
	if err != nil {
		t.Fatal(err)
	}
	info, err := w.CreateSession(ctx, ctrlproto.CreateOpts{Experience: "chat", Card: imported.ID})
	if err != nil {
		t.Fatal(err)
	}
	live := w.live(info.ID)
	if got := reviseTexts(live.agent.Messages()); len(got) != 1 || !strings.Contains(got[0], "Kira") {
		t.Errorf("default-persona greeting = %v, want it to contain Kira", got)
	}
	if live.sess.Meta.UserName != "Kira" {
		t.Errorf("session user name = %q, want Kira (the default, stamped in meta)", live.sess.Meta.UserName)
	}

	// Binding a saved persona by ref applies its name + description.
	if err := w.UserBind(ctx, info.ID, ctrlproto.UserBindParams{Ref: "aria"}); err != nil {
		t.Fatal(err)
	}
	if live.sess.Meta.UserName != "Aria" {
		t.Errorf("after bind ref, user name = %q, want Aria", live.sess.Meta.UserName)
	}
	if err := w.UserBind(ctx, info.ID, ctrlproto.UserBindParams{Ref: "nope"}); err == nil {
		t.Error("binding a missing saved persona should error")
	}

	// A coding session gets no default persona (immersive-only).
	coding, err := w.CreateSession(ctx, ctrlproto.CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if w.live(coding.ID).sess.Meta.UserName != "" {
		t.Error("a coding session must not receive the default user persona")
	}

	// Delete removes one.
	if err := w.UserPersonaDelete(ctx, ctrlproto.UserPersonaRef{Ref: "aria"}); err != nil {
		t.Fatal(err)
	}
	if list, _ := w.UserPersonasList(ctx); len(list.Personas) != 1 {
		t.Errorf("after delete: %+v, want 1", list.Personas)
	}
}

// Renaming a saved persona: the ref says WHICH persona is being edited, so a
// changed name moves that one rather than leaving the old name behind as a
// second, stale copy of you.
func TestUserPersonaRename(t *testing.T) {
	w := draftWorkspace(t)
	ctx := context.Background()

	if _, err := w.UserPersonaSave(ctx, ctrlproto.UserPersonaView{Name: "Kira", Description: "A wary courier."}); err != nil {
		t.Fatal(err)
	}
	if err := w.UserPersonaSetDefault(ctx, ctrlproto.UserPersonaRef{Ref: "kira"}); err != nil {
		t.Fatal(err)
	}

	saved, err := w.UserPersonaSave(ctx, ctrlproto.UserPersonaView{Ref: "kira", Name: "Kiran", Description: "A wary courier."})
	if err != nil {
		t.Fatal(err)
	}
	if saved.Ref != "kiran" {
		t.Errorf("renamed ref = %q, want kiran", saved.Ref)
	}
	// The rename is a MOVE: exactly one persona, under the new name.
	list, _ := w.UserPersonasList(ctx)
	if len(list.Personas) != 1 || list.Personas[0].Name != "Kiran" {
		t.Fatalf("after rename: %+v, want just Kiran", list.Personas)
	}
	// Default status is the persona's, not the name's — it rides the rename, so a
	// renamed default still pre-fills new chats.
	if !list.Personas[0].Default {
		t.Error("the renamed persona should still be the default")
	}

	// An unrefed save is still keyed by name alone (the old, ref-less clients):
	// a new name is a new persona, not a rename.
	if _, err := w.UserPersonaSave(ctx, ctrlproto.UserPersonaView{Name: "Mara"}); err != nil {
		t.Fatal(err)
	}
	if list, _ := w.UserPersonasList(ctx); len(list.Personas) != 2 {
		t.Fatalf("unrefed save: %+v, want 2 personas", list.Personas)
	}

	// Renaming ONTO an existing persona would overwrite it and then delete the
	// one being renamed — two rows lost to a typo. Refused, and both survive.
	if _, err := w.UserPersonaSave(ctx, ctrlproto.UserPersonaView{Ref: "kiran", Name: "Mara", Description: "clobber"}); err == nil {
		t.Error("renaming onto an existing persona should be refused")
	}
	list, _ = w.UserPersonasList(ctx)
	if len(list.Personas) != 2 {
		t.Fatalf("after the refused rename: %+v, want both personas intact", list.Personas)
	}
	if mara, err := w.userPersonaStore().Get("mara"); err != nil || mara.Description != "" {
		t.Errorf("Mara = %+v (%v), want her own (empty) description, unclobbered", mara, err)
	}

	// An in-place edit — same ref, same name — is not a collision with itself.
	if _, err := w.UserPersonaSave(ctx, ctrlproto.UserPersonaView{Ref: "kiran", Name: "Kiran", Description: "Wearier now."}); err != nil {
		t.Fatal(err)
	}
	got, err := w.userPersonaStore().Get("kiran")
	if err != nil || got.Description != "Wearier now." {
		t.Errorf("in-place edit = %+v (%v), want the new description", got, err)
	}
	if !got.Default {
		t.Error("an in-place edit must not disturb default status")
	}

	// A ref that no longer resolves falls back to a create — the author's text
	// outranks the bookkeeping.
	if _, err := w.UserPersonaSave(ctx, ctrlproto.UserPersonaView{Ref: "ghost", Name: "Vess"}); err != nil {
		t.Fatal(err)
	}
	if list, _ := w.UserPersonasList(ctx); len(list.Personas) != 3 {
		t.Errorf("stale-ref save: %+v, want 3 personas", list.Personas)
	}
}
