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
