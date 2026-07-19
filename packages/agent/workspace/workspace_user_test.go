package workspace

import (
	"context"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

// A card whose description and greeting both carry the {{user}} macro, so a
// bound name is observable in the cached prefix (charter) and the tail frame.
const userCardSrc = `{"name":"Nova","first_mes":"Hi there, {{user}}.","description":"Nova has known {{user}} for years."}`

// TestWorkspaceUserBind drives user.bind end to end on a live card session: the
// description rides the free per-turn tail (framed with the name), a changed name
// re-bakes the {{user}} macro into the cached charter, both surface on
// SessionInfo, and everything clears cleanly. A description-only edit leaves the
// prefix untouched; a coding session and a saved-ref bind are bad requests.
func TestWorkspaceUserBind(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	t.Setenv("OPENAI_API_KEY", "test-key")
	w, err := NewWorkspace(build.Args{Provider: "openai", Model: "gpt-5", CWD: testsupport.TempDir(t)}, "test")
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	ctx := context.Background()

	imported, err := w.CardsImport(ctx, ctrlproto.CardImportParams{Bytes: []byte(userCardSrc)})
	if err != nil {
		t.Fatal(err)
	}
	info, err := w.CreateSession(ctx, ctrlproto.CreateOpts{Experience: "chat", Card: imported.ID})
	if err != nil {
		t.Fatal(err)
	}
	live := w.live(info.ID)

	// The {{user}} macro defaults to "User" before any bind.
	if !strings.Contains(live.agent.System, "known User for years") {
		t.Fatalf("charter did not resolve the default {{user}}: %q", live.agent.System)
	}

	const name = "Kira"
	const desc = "a wary courier who trusts no one"
	if err := w.UserBind(ctx, info.ID, ctrlproto.UserBindParams{Name: name, Description: desc}); err != nil {
		t.Fatal(err)
	}

	// Persisted to meta and surfaced on SessionInfo.
	if live.sess.Meta.UserName != name || live.sess.Meta.UserDescription != desc {
		t.Errorf("meta = {%q,%q}, want {%q,%q}", live.sess.Meta.UserName, live.sess.Meta.UserDescription, name, desc)
	}
	if si := live.info(); si.UserName != name || si.UserDescription != desc {
		t.Errorf("SessionInfo = {%q,%q}, want {%q,%q}", si.UserName, si.UserDescription, name, desc)
	}
	// The NAME re-baked into the cached charter (a deliberate prefix rebuild).
	if !strings.Contains(live.agent.System, "known Kira for years") {
		t.Errorf("bound name did not bake into the charter: %q", live.agent.System)
	}
	if !strings.Contains(live.args.As, name) {
		t.Errorf("args.As = %q, want %q (threaded for the next rebuild/restart)", live.args.As, name)
	}
	// The DESCRIPTION rides the per-turn tail, framed with the name.
	cp := live.agent.ContextProvider
	if cp == nil || !strings.Contains(cp(), desc) || !strings.Contains(cp(), "About Kira (the user you are interacting with):") {
		t.Errorf("user description / frame not in the tail: %q", func() string {
			if cp == nil {
				return "<nil>"
			}
			return cp()
		}())
	}

	// A description-only edit is a free tail update — the prefix must not change.
	sysBefore := live.agent.System
	if err := w.UserBind(ctx, info.ID, ctrlproto.UserBindParams{Name: name, Description: "a battle-scarred veteran"}); err != nil {
		t.Fatal(err)
	}
	if live.agent.System != sysBefore {
		t.Error("a description-only bind rebuilt the cached prefix; it should be free")
	}
	if !strings.Contains(live.agent.ContextProvider(), "a battle-scarred veteran") {
		t.Error("description-only edit not reflected in the tail")
	}

	// Clearing both halves empties meta, drops the description from the tail, and
	// returns the {{user}} macro to the default.
	if err := w.UserBind(ctx, info.ID, ctrlproto.UserBindParams{}); err != nil {
		t.Fatal(err)
	}
	if live.sess.Meta.UserName != "" || live.sess.Meta.UserDescription != "" {
		t.Errorf("user persona not cleared: %+v", live.sess.Meta)
	}
	if strings.Contains(live.agent.ContextProvider(), "veteran") {
		t.Error("cleared description still in the tail")
	}
	if !strings.Contains(live.agent.System, "known User for years") {
		t.Errorf("charter did not return to the default {{user}} on clear: %q", live.agent.System)
	}

	// A saved-persona ref is reserved, not implemented -> bad request.
	if err := w.UserBind(ctx, info.ID, ctrlproto.UserBindParams{Ref: "some-persona"}); err == nil {
		t.Error("binding a saved user-persona ref should be a bad request for now")
	}

	// A coding (non-immersive) session carries no user-persona record -> bad request.
	coding, err := w.CreateSession(ctx, ctrlproto.CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.UserBind(ctx, coding.ID, ctrlproto.UserBindParams{Name: "x"}); err == nil {
		t.Error("user.bind on a coding session should be a bad request")
	}
}

// TestUserPersonaReseedsOnResume proves restart durability for both halves: a
// name+description bound on one workspace re-materializes on a fresh workspace —
// the name baked back into the charter (via meta.UserName -> Args.As) and the
// description re-seeded into the per-turn tail.
func TestUserPersonaReseedsOnResume(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	t.Setenv("OPENAI_API_KEY", "test-key")
	cwd := testsupport.TempDir(t)
	w1, err := NewWorkspace(build.Args{Provider: "openai", Model: "gpt-5", CWD: cwd}, "test")
	if err != nil {
		t.Fatal(err)
	}
	imported, err := w1.CardsImport(context.Background(), ctrlproto.CardImportParams{Bytes: []byte(userCardSrc)})
	if err != nil {
		t.Fatal(err)
	}
	info, err := w1.CreateSession(context.Background(), ctrlproto.CreateOpts{Experience: "chat", Card: imported.ID})
	if err != nil {
		t.Fatal(err)
	}
	const name = "Kira"
	const desc = "a wary courier who trusts no one"
	if err := w1.UserBind(context.Background(), info.ID, ctrlproto.UserBindParams{Name: name, Description: desc}); err != nil {
		t.Fatal(err)
	}
	// Seed a message so the session is not pruned on close (meta rows alone prune).
	if err := w1.live(info.ID).sess.AppendMessage(swipeMsg(provider.RoleUser, "hello")); err != nil {
		t.Fatal(err)
	}
	w1.Close()

	w2, err := NewWorkspace(build.Args{Provider: "openai", Model: "gpt-5", CWD: cwd}, "test")
	if err != nil {
		t.Fatal(err)
	}
	defer w2.Close()
	if _, err := w2.ResumeSession(context.Background(), info.ID); err != nil {
		t.Fatalf("resume: %v", err)
	}
	live := w2.live(info.ID)
	if live == nil {
		t.Fatal("resumed session is not live")
	}
	// The name is back in the cached charter after a cold rebuild.
	if !strings.Contains(live.agent.System, "known Kira for years") {
		t.Errorf("name not re-baked into the charter on restart: %q", live.agent.System)
	}
	// The description is back in the tail.
	if live.user == nil || live.user.Get() != desc {
		t.Errorf("user description not reseeded on restart: %v", live.user)
	}
	if cp := live.agent.ContextProvider; cp == nil || !strings.Contains(cp(), desc) {
		t.Error("reseeded user description not injected into the tail after restart")
	}
}

// TestReloadLorePreservesTailRecords guards the pre-4b re-pointing bug: a lore
// reload swaps the per-turn provider to a fresh Resolve, which mints new note /
// user-persona records — so the retained handles must be re-pointed AND re-seeded
// from meta, or the surface writes a record the tail no longer reads. Before the
// rewireTail fix the author's note (and now the user description) went silent
// after any lore edit.
func TestReloadLorePreservesTailRecords(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	t.Setenv("OPENAI_API_KEY", "test-key")
	w, err := NewWorkspace(build.Args{Provider: "openai", Model: "gpt-5", CWD: testsupport.TempDir(t)}, "test")
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	ctx := context.Background()

	info, err := w.CreateSession(ctx, ctrlproto.CreateOpts{Experience: "chat"})
	if err != nil {
		t.Fatal(err)
	}
	live := w.live(info.ID)

	const note = "It is raining hard."
	const desc = "a wary courier"
	if err := w.NoteSet(ctx, info.ID, ctrlproto.NoteSetParams{Text: note}); err != nil {
		t.Fatal(err)
	}
	if err := w.UserBind(ctx, info.ID, ctrlproto.UserBindParams{Description: desc}); err != nil {
		t.Fatal(err)
	}

	// A lore reload swaps the tail provider under the agent.
	live.reloadLore()

	// The retained handles now point at the freshly-Resolved records...
	if live.note == nil || live.note.Get() != note {
		t.Errorf("note handle orphaned by reloadLore: %v", live.note)
	}
	if live.user == nil || live.user.Get() != desc {
		t.Errorf("user handle orphaned by reloadLore: %v", live.user)
	}
	// ...and the record the NEW tail reads still carries both.
	cp := live.agent.ContextProvider
	if cp == nil {
		t.Fatal("no context provider after reloadLore")
	}
	if out := cp(); !strings.Contains(out, note) || !strings.Contains(out, desc) {
		t.Errorf("note/description lost from the tail after reloadLore: %q", out)
	}
}
