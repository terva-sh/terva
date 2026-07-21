package workspace

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
)

// World lore (Worlds L1): world.lore.put / world.lore.delete persist to the
// session meta (last-wins row), update the live record the per-turn tail
// scans, and surface through SessionInfo.WorldLore.
func TestWorldLorePutDeletePersists(t *testing.T) {
	w := draftWorkspace(t)
	ctx := context.Background()

	imported, err := w.CardsImport(ctx, ctrlproto.CardImportParams{Bytes: []byte(`{"name":"Elira","first_mes":"You kept me waiting."}`)})
	if err != nil {
		t.Fatal(err)
	}
	info, err := w.CreateSession(ctx, ctrlproto.CreateOpts{Experience: "chat", Card: imported.ID})
	if err != nil {
		t.Fatal(err)
	}

	put := func(p ctrlproto.WorldLorePutParams) error { return w.WorldLorePut(ctx, info.ID, p) }
	if err := put(ctrlproto.WorldLorePutParams{Entry: ctrlproto.WorldLoreEntry{Name: "The Accord", Constant: true, Content: "Magic is outlawed."}}); err != nil {
		t.Fatalf("put constant: %v", err)
	}
	if err := put(ctrlproto.WorldLorePutParams{Entry: ctrlproto.WorldLoreEntry{Name: "The debt", Keys: []string{"debt", " guild "}, Content: "Elira owes the guild."}}); err != nil {
		t.Fatalf("put keyed: %v", err)
	}

	live := w.live(info.ID)
	if got := live.info().WorldLore; len(got) != 2 || got[0].Name != "The Accord" || got[1].Keys[1] != "guild" {
		t.Fatalf("SessionInfo.WorldLore = %+v (keys should be trimmed, order = insertion)", got)
	}
	if got := live.worldLore.Get(); len(got) != 2 || got[0].Source != "world" {
		t.Fatalf("live record should hold engine entries tagged world, got %+v", got)
	}

	// Upsert by name edits in place; Replace renames in place.
	if err := put(ctrlproto.WorldLorePutParams{Entry: ctrlproto.WorldLoreEntry{Name: "The Accord", Constant: true, Content: "The Accord has fallen."}}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := put(ctrlproto.WorldLorePutParams{Entry: ctrlproto.WorldLoreEntry{Name: "Guild debt", Keys: []string{"debt"}, Content: "Elira owes the guild."}, Replace: "The debt"}); err != nil {
		t.Fatalf("rename: %v", err)
	}
	wl := live.sess.Meta.WorldLore
	if len(wl) != 2 || wl[0].Content != "The Accord has fallen." || wl[1].Name != "Guild debt" {
		t.Fatalf("after upsert+rename, meta = %+v", wl)
	}
	// A rename may not shadow an existing entry.
	if err := put(ctrlproto.WorldLorePutParams{Entry: ctrlproto.WorldLoreEntry{Name: "Guild debt", Keys: []string{"x"}, Content: "dupe"}, Replace: "The Accord"}); err == nil {
		t.Error("renaming onto an existing entry's name should be rejected")
	}

	// Validation: no name / no content / no keys-nor-constant are bad requests.
	for _, bad := range []ctrlproto.WorldLoreEntry{
		{Name: "", Constant: true, Content: "x"},
		{Name: "Empty", Constant: true, Content: "  "},
		{Name: "Inert", Content: "could never fire"},
	} {
		if err := put(ctrlproto.WorldLorePutParams{Entry: bad}); err == nil {
			t.Errorf("entry %+v should be rejected", bad)
		}
	}

	// Delete removes by name; a miss is a bad request.
	if err := w.WorldLoreDelete(ctx, info.ID, ctrlproto.WorldLoreDeleteParams{Name: "Guild debt"}); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := w.WorldLoreDelete(ctx, info.ID, ctrlproto.WorldLoreDeleteParams{Name: "Guild debt"}); err == nil {
		t.Error("deleting a missing entry should be rejected")
	}
	if got := live.sess.Meta.WorldLore; len(got) != 1 || got[0].Name != "The Accord" {
		t.Fatalf("after delete, meta = %+v", got)
	}
	if got := live.worldLore.Get(); len(got) != 1 {
		t.Fatalf("live record should track the delete, got %+v", got)
	}
}

// The L2 visibility rule, in one table: a world-shared entry reaches everyone,
// a targeted entry only its audience (whitespace/case-forgiving), and the
// scene authority ("") sees everything.
func TestWorldLoreFor(t *testing.T) {
	entries := []core.WorldLoreEntry{
		{Name: "world", Constant: true, Content: "shared"},
		{Name: "elira-secret", Keys: []string{"debt"}, Content: "x", Audience: []string{" elira "}},
		{Name: "pair-secret", Keys: []string{"plan"}, Content: "y", Audience: []string{"Elira", "Rook"}},
	}
	names := func(es []core.WorldLoreEntry) string {
		var out []string
		for _, e := range es {
			out = append(out, e.Name)
		}
		return strings.Join(out, ",")
	}
	if got := names(worldLoreFor(entries, "")); got != "world,elira-secret,pair-secret" {
		t.Errorf("the scene authority sees everything, got %s", got)
	}
	if got := names(worldLoreFor(entries, "Elira")); got != "world,elira-secret,pair-secret" {
		t.Errorf("Elira sees world + her secrets, got %s", got)
	}
	if got := names(worldLoreFor(entries, "Rook")); got != "world,pair-secret" {
		t.Errorf("Rook must NOT see elira-secret, got %s", got)
	}
	if got := names(worldLoreFor(entries, "Stranger")); got != "world" {
		t.Errorf("an unnamed character sees only world lore, got %s", got)
	}
}

// End to end through the real put path: the pin's drift reaches SessionInfo,
// survives an unrelated lore write, and clears when the card is rewritten.
func TestScenePinStaleReachesSessionInfo(t *testing.T) {
	w := draftWorkspace(t)
	ctx := context.Background()
	imported, err := w.CardsImport(ctx, ctrlproto.CardImportParams{Bytes: []byte(`{"name":"Kobeni","first_mes":"Hello."}`)})
	if err != nil {
		t.Fatal(err)
	}
	info, err := w.CreateSession(ctx, ctrlproto.CreateOpts{Experience: "chat", Card: imported.ID})
	if err != nil {
		t.Fatal(err)
	}
	put := func(e ctrlproto.WorldLoreEntry) {
		t.Helper()
		if err := w.WorldLorePut(ctx, info.ID, ctrlproto.WorldLorePutParams{Entry: e}); err != nil {
			t.Fatalf("put %s: %v", e.Name, err)
		}
	}
	live := w.live(info.ID)

	// No pin: the field must be absent, not "0 turns stale" — a session with no
	// card must never read as one with a current card.
	put(ctrlproto.WorldLoreEntry{Name: "The Accord", Constant: true, Content: "Magic is outlawed."})
	if got := live.info().ScenePinStale; got != 0 {
		t.Fatalf("no pin should report 0, got %d", got)
	}

	// Pin it against an explicit baseline — the session already carries the
	// card's greeting, so "messages since the pin" is only meaningful relative
	// to the count at the moment it was written.
	beats := func(n int) {
		t.Helper()
		msgs := make([]provider.Message, n)
		for i := range msgs {
			msgs[i] = provider.Message{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: "beat"}}}
		}
		live.agent.SetMessages(msgs)
	}
	beats(4)
	put(ctrlproto.WorldLoreEntry{Name: core.SceneStateName, Content: "Veyra waits outside the locked door."})
	if got := live.info().ScenePinStale; got != 0 {
		t.Fatalf("a just-written pin has no drift, got %d", got)
	}
	beats(12)
	if got := live.info().ScenePinStale; got != 8 {
		t.Fatalf("drift after 8 further messages = %d, want 8", got)
	}

	// An unrelated lore write must NOT re-date the pin — otherwise the drift
	// resets every time the author touches anything else in the World tab.
	put(ctrlproto.WorldLoreEntry{Name: "The debt", Keys: []string{"debt"}, Content: "Three favors owed."})
	if got := live.info().ScenePinStale; got != 8 {
		t.Fatalf("an unrelated write must not refresh the pin, drift = %d, want 8", got)
	}

	// Rewriting the card clears it.
	put(ctrlproto.WorldLoreEntry{Name: core.SceneStateName, Content: "Veyra is inside; the requisition is signed."})
	if got := live.info().ScenePinStale; got != 0 {
		t.Fatalf("a rewritten pin clears the drift, got %d", got)
	}
}

// The scene-state pin is dated by CONTENT CHANGE, not by write (SD6). The
// distinction is the whole signal: the pin claims to outrank disagreeing
// history, nothing keeps it current on its own, and a session that re-dated it
// every time an unrelated entry was added would report a card as fresh for
// exactly as long as the author kept touching other lore.
func TestStampScenePin(t *testing.T) {
	pin := func(es []core.WorldLoreEntry) core.WorldLoreEntry {
		for _, e := range es {
			if core.IsSceneState(e.Name) {
				return e
			}
		}
		t.Fatalf("no pin in %+v", es)
		return core.WorldLoreEntry{}
	}

	// First write of the pin dates it at the current message count.
	prev := []core.WorldLoreEntry{{Name: "The bell", Keys: []string{"bell"}, Content: "Rings at dusk."}}
	next := append(append([]core.WorldLoreEntry(nil), prev...),
		core.WorldLoreEntry{Name: core.SceneStateName, Constant: true, Content: "Day 14, first light."})
	got := stampScenePin(next, prev, 6)
	if pin(got).PinnedAt != 6 {
		t.Fatalf("a new pin dates at the write, got %d", pin(got).PinnedAt)
	}

	// Eight messages later the author adds an UNRELATED entry. The pin is
	// rewritten in the list but its content is identical — it keeps its date,
	// and the drift keeps growing.
	prev = got
	next = append(append([]core.WorldLoreEntry(nil), prev...),
		core.WorldLoreEntry{Name: "The debt", Keys: []string{"debt"}, Content: "Three favors owed."})
	got = stampScenePin(next, prev, 14)
	if pin(got).PinnedAt != 6 {
		t.Errorf("an untouched pin must keep its date, got %d", pin(got).PinnedAt)
	}
	if turns, pinned := scenePinDrift(got, 14); !pinned || turns != 8 {
		t.Errorf("drift = %d (pinned %v), want 8", turns, pinned)
	}

	// Rewriting the content re-dates it, and the drift resets.
	prev = got
	next = []core.WorldLoreEntry{{Name: core.SceneStateName, Constant: true, Content: "Day 14, midmorning. Veyra is inside."}}
	got = stampScenePin(next, prev, 14)
	if pin(got).PinnedAt != 14 {
		t.Errorf("a rewritten pin re-dates, got %d", pin(got).PinnedAt)
	}
	if turns, _ := scenePinDrift(got, 14); turns != 0 {
		t.Errorf("a just-written pin has no drift, got %d", turns)
	}

	// No pin at all is not "a pin with zero drift" — the caller must be able to
	// tell them apart, or a session with no card reads as a current one.
	if turns, pinned := scenePinDrift(prev[:1], 14); pinned && turns == 0 {
		if !core.IsSceneState(prev[0].Name) {
			t.Errorf("a session with no pin must report pinned=false")
		}
	}
	if _, pinned := scenePinDrift([]core.WorldLoreEntry{{Name: "The bell", Content: "x"}}, 14); pinned {
		t.Error("a session with no pin must report pinned=false")
	}
	// A pin dated ahead of the count (hand-edited meta, imported bundle) clamps.
	if turns, pinned := scenePinDrift([]core.WorldLoreEntry{
		{Name: core.SceneStateName, Constant: true, Content: "x", PinnedAt: 99},
	}, 14); !pinned || turns != 0 {
		t.Errorf("a pin dated ahead must clamp to 0, got %d", turns)
	}
}

// Audience persists through put (trimmed/deduped), surfaces on SessionInfo,
// and — the structural guard — the session tail's record holds only what the
// BOUND character is cleared for.
func TestWorldLoreAudienceFiltersTail(t *testing.T) {
	w := draftWorkspace(t)
	ctx := context.Background()
	imported, err := w.CardsImport(ctx, ctrlproto.CardImportParams{Bytes: []byte(`{"name":"Elira","first_mes":"hi"}`)})
	if err != nil {
		t.Fatal(err)
	}
	info, err := w.CreateSession(ctx, ctrlproto.CreateOpts{Experience: "chat", Card: imported.ID})
	if err != nil {
		t.Fatal(err)
	}
	put := func(e ctrlproto.WorldLoreEntry) {
		t.Helper()
		if err := w.WorldLorePut(ctx, info.ID, ctrlproto.WorldLorePutParams{Entry: e}); err != nil {
			t.Fatal(err)
		}
	}
	put(ctrlproto.WorldLoreEntry{Name: "world", Constant: true, Content: "shared"})
	put(ctrlproto.WorldLoreEntry{Name: "hers", Constant: true, Content: "x", Audience: []string{" Elira", "elira", ""}})
	put(ctrlproto.WorldLoreEntry{Name: "rooks", Constant: true, Content: "y", Audience: []string{"Rook"}})

	live := w.live(info.ID)
	// Deduped + trimmed on the way in.
	if got := live.sess.Meta.WorldLore[1].Audience; len(got) != 1 || got[0] != "Elira" {
		t.Errorf("audience should be trimmed+deduped, got %v", got)
	}
	// The user's editing view keeps every entry (they author the secrets).
	if got := live.info().WorldLore; len(got) != 3 || len(got[2].Audience) != 1 {
		t.Fatalf("SessionInfo.WorldLore should carry all entries + audience, got %+v", got)
	}
	// The tail record — what the bound character (Elira) generates with — holds
	// world + hers, never Rook's secret.
	rec := live.worldLore.Get()
	if len(rec) != 2 || rec[0].Name != "world" || rec[1].Name != "hers" {
		t.Fatalf("the bound character's tail must exclude other characters' secrets, got %+v", rec)
	}
}

// Promotion (W5): worlds.save lifts the session's World into the library and
// stamps membership; a member session's save updates the SAME World in place
// (explicit save-back, name preserved); creating a session inside the World
// copies the whole state back out.
func TestWorldSavePromoteUpdateAndCreateIn(t *testing.T) {
	w := draftWorkspace(t)
	ctx := context.Background()
	imported, err := w.CardsImport(ctx, ctrlproto.CardImportParams{Bytes: []byte(`{"name":"Elira","first_mes":"hi"}`)})
	if err != nil {
		t.Fatal(err)
	}
	rook, err := w.CardsImport(ctx, ctrlproto.CardImportParams{Bytes: []byte(`{"name":"Rook","first_mes":"Trouble?"}`)})
	if err != nil {
		t.Fatal(err)
	}
	info, err := w.CreateSession(ctx, ctrlproto.CreateOpts{Experience: "chat", Card: imported.ID})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.CastAdd(ctx, info.ID, ctrlproto.CastMemberParams{Name: "Rook", Ref: rook.ID}); err != nil {
		t.Fatal(err)
	}
	// A posted line un-drafts the session (drafts are unlisted, so they don't
	// count toward a World's sessions — matching the session list).
	if err := w.PostLine(ctx, info.ID, ctrlproto.PostLineParams{Text: "The scene opens."}); err != nil {
		t.Fatal(err)
	}
	if err := w.WorldLorePut(ctx, info.ID, ctrlproto.WorldLorePutParams{Entry: ctrlproto.WorldLoreEntry{Name: "The Accord", Constant: true, Content: "Magic is outlawed."}}); err != nil {
		t.Fatal(err)
	}
	if err := w.WorldSet(ctx, info.ID, ctrlproto.WorldSetParams{Coordination: "focus:Rook"}); err != nil {
		t.Fatal(err)
	}

	// First save needs a name; then it mints + stamps membership.
	if _, err := w.WorldSave(ctx, info.ID, ctrlproto.WorldSaveParams{}); err == nil {
		t.Error("first save without a name should be refused")
	}
	view, err := w.WorldSave(ctx, info.ID, ctrlproto.WorldSaveParams{Name: "Lowtown"})
	if err != nil {
		t.Fatal(err)
	}
	live := w.live(info.ID)
	if live.info().World != view.ID {
		t.Fatalf("promotion should stamp membership, got %q", live.info().World)
	}
	doc, err := build.NewWorldStore().Get(view.ID)
	if err != nil {
		t.Fatal(err)
	}
	if doc.Characters["Rook"] != rook.ID || len(doc.Lore) != 1 || doc.Coordination != "focus:Rook" {
		t.Fatalf("promoted doc = %+v", doc)
	}

	// Save-back: mutate the session's lore, save with NO name — same World,
	// updated contents, name kept.
	if err := w.WorldLorePut(ctx, info.ID, ctrlproto.WorldLorePutParams{Entry: ctrlproto.WorldLoreEntry{Name: "The Watch", Keys: []string{"watch"}, Content: "They answer to the guild."}}); err != nil {
		t.Fatal(err)
	}
	again, err := w.WorldSave(ctx, info.ID, ctrlproto.WorldSaveParams{})
	if err != nil {
		t.Fatal(err)
	}
	if again.ID != view.ID || again.Name != "Lowtown" {
		t.Fatalf("save-back must update in place: %+v", again)
	}
	if doc, _ := build.NewWorldStore().Get(view.ID); len(doc.Lore) != 2 {
		t.Errorf("save-back should persist the new entry, got %d", len(doc.Lore))
	}

	// Create IN the World: roster, lore, coordination, membership all seed.
	inWorld, err := w.CreateSession(ctx, ctrlproto.CreateOpts{Card: imported.ID, World: view.ID})
	if err != nil {
		t.Fatal(err)
	}
	member := w.live(inWorld.ID)
	mi := member.info()
	if mi.World != view.ID || mi.Experience != "chat" {
		t.Errorf("member session info = world %q experience %q", mi.World, mi.Experience)
	}
	if mi.Cast["Rook"] != rook.ID || len(mi.WorldLore) != 2 || mi.Coordination != "focus:Rook" {
		t.Errorf("member session should seed the World's state: %+v", mi)
	}
	if rec := member.worldLore.Get(); len(rec) != 2 {
		t.Errorf("the member's live lore record should be seeded, got %d", len(rec))
	}

	// The shelf: one World; the promoted session counts, the fresh member is
	// still a draft (unlisted) so it doesn't.
	list, err := w.WorldsList(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Worlds) != 1 || list.Worlds[0].Sessions != 1 {
		t.Fatalf("worlds list = %+v", list.Worlds)
	}

	// Deleting the World keeps the sessions (they lose only the grouping).
	if err := w.WorldDelete(ctx, ctrlproto.WorldDeleteParams{ID: view.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.CreateSession(ctx, ctrlproto.CreateOpts{Card: imported.ID, World: view.ID}); err == nil {
		t.Error("creating in a deleted World should be refused")
	}
	if got := member.sess.Meta.WorldLore; len(got) != 2 {
		t.Errorf("a member session keeps its copy after delete, got %d entries", len(got))
	}
}

// A coding session carries no World: the verbs answer bad-request, mirroring
// the author's-note gate.
func TestWorldLoreRejectsCodingSession(t *testing.T) {
	w := draftWorkspace(t)
	ctx := context.Background()
	info, err := w.CreateSession(ctx, ctrlproto.CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}
	err = w.WorldLorePut(ctx, info.ID, ctrlproto.WorldLorePutParams{Entry: ctrlproto.WorldLoreEntry{Name: "x", Constant: true, Content: "y"}})
	if err == nil || !strings.Contains(err.Error(), "chat/play") {
		t.Errorf("a coding session should reject world lore, got %v", err)
	}
}

// The W5b bundle, export half: worlds.export lifts the saved World plus every
// roster character's card — the bound character included (WorldSave merges
// them into the roster: the World is the whole stage, not just the cast).
func TestWorldExportBundlesStage(t *testing.T) {
	w := draftWorkspace(t)
	ctx := context.Background()
	elira, err := w.CardsImport(ctx, ctrlproto.CardImportParams{Bytes: []byte(`{"name":"Elira","first_mes":"hi"}`)})
	if err != nil {
		t.Fatal(err)
	}
	rook, err := w.CardsImport(ctx, ctrlproto.CardImportParams{Bytes: []byte(`{"name":"Rook","first_mes":"Trouble?"}`)})
	if err != nil {
		t.Fatal(err)
	}
	info, err := w.CreateSession(ctx, ctrlproto.CreateOpts{Experience: "chat", Card: elira.ID})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.CastAdd(ctx, info.ID, ctrlproto.CastMemberParams{Name: "Rook", Ref: rook.ID}); err != nil {
		t.Fatal(err)
	}
	if err := w.WorldLorePut(ctx, info.ID, ctrlproto.WorldLorePutParams{Entry: ctrlproto.WorldLoreEntry{Name: "The Accord", Constant: true, Content: "Magic is outlawed.", Audience: []string{"Rook"}}}); err != nil {
		t.Fatal(err)
	}
	view, err := w.WorldSave(ctx, info.ID, ctrlproto.WorldSaveParams{Name: "Lowtown"})
	if err != nil {
		t.Fatal(err)
	}
	if view.Characters["Elira"] != elira.ID || view.Characters["Rook"] != rook.ID {
		t.Fatalf("the saved roster should carry the whole stage: %+v", view.Characters)
	}

	exp, err := w.WorldsExport(ctx, ctrlproto.WorldExportParams{ID: view.ID})
	if err != nil {
		t.Fatal(err)
	}
	if exp.Filename != "Lowtown.world.json" || exp.MimeType != "application/json" {
		t.Errorf("export = %q %q", exp.Filename, exp.MimeType)
	}
	var bundle build.WorldBundle
	if err := json.Unmarshal(exp.Bytes, &bundle); err != nil {
		t.Fatal(err)
	}
	if bundle.Spec != build.WorldBundleSpec || bundle.World.Name != "Lowtown" {
		t.Fatalf("bundle head = %q %q", bundle.Spec, bundle.World.Name)
	}
	if len(bundle.Cards) != 2 {
		t.Fatalf("bundle should embed both characters' cards, got %d", len(bundle.Cards))
	}
	if len(bundle.World.Lore) != 1 || bundle.World.Lore[0].Audience[0] != "Rook" {
		t.Fatalf("bundle lore = %+v", bundle.World.Lore)
	}
}

// The W5b bundle, import half: embedded cards land in the card library, the
// roster is remapped from the exporting library's ids, an unresolvable member
// is dropped (with its model pin), the lore's audience + learned axes ride
// through, the cover lands, and a FRESH id is always minted.
func TestWorldsImportRemapsAndMints(t *testing.T) {
	w := draftWorkspace(t)
	ctx := context.Background()
	bundle := build.WorldBundle{
		Spec: build.WorldBundleSpec,
		World: build.WorldDoc{
			ID:   "lowtown-deadbeef",
			Name: "Lowtown",
			Characters: map[string]string{
				"Elira": "foreign-1",
				"Ghost": "foreign-2", // no card in the bundle, none local — dropped
			},
			CharacterModels: map[string]core.CastRoute{
				"Elira": {Provider: "anthropic", Model: "m"},
				"Ghost": {Provider: "anthropic", Model: "m"},
			},
			Lore:         []core.WorldLoreEntry{{Name: "Secret", Keys: []string{"vault"}, Content: "c", Audience: []string{"Elira"}, Learned: map[string]string{"Elira": "2026-07-19T00:00:00Z"}}},
			Coordination: "off",
		},
		Cards: []build.BundleCard{{Ref: "foreign-1", Name: "Elira", Mime: "application/json", Bytes: []byte(`{"name":"Elira","first_mes":"hi"}`)}},
		Cover: []byte("png-bytes"),
	}
	raw, err := json.Marshal(bundle)
	if err != nil {
		t.Fatal(err)
	}
	view, err := w.WorldsImport(ctx, ctrlproto.WorldImportParams{Bytes: raw})
	if err != nil {
		t.Fatal(err)
	}
	if view.ID == "" || view.ID == "lowtown-deadbeef" {
		t.Fatalf("import must mint a fresh id, got %q", view.ID)
	}
	newRef, ok := view.Characters["Elira"]
	if !ok || newRef == "foreign-1" {
		t.Fatalf("Elira should be remapped to a local card id, got %q", newRef)
	}
	if _, err := w.cardStore().Get(newRef); err != nil {
		t.Fatalf("the remapped ref should resolve locally: %v", err)
	}
	if _, ok := view.Characters["Ghost"]; ok {
		t.Error("an unresolvable member should be dropped")
	}
	doc, err := build.NewWorldStore().Get(view.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := doc.CharacterModels["Ghost"]; ok {
		t.Error("a dropped member's model pin should go with it")
	}
	if _, ok := doc.CharacterModels["Elira"]; !ok {
		t.Error("a kept member's model pin should survive")
	}
	if len(doc.Lore) != 1 || doc.Lore[0].Audience[0] != "Elira" || doc.Lore[0].Learned["Elira"] == "" {
		t.Fatalf("imported lore = %+v", doc.Lore)
	}
	if view.CoverURL == "" || build.NewWorldStore().CoverPath(view.ID) == "" {
		t.Error("the bundle's cover should land")
	}

	// A second import of the same bundle: cards dedupe (content-hashed ids)
	// but the World minted is a NEW one — imports never overwrite.
	again, err := w.WorldsImport(ctx, ctrlproto.WorldImportParams{Bytes: raw})
	if err != nil {
		t.Fatal(err)
	}
	if again.ID == view.ID {
		t.Error("re-import must mint another World, not overwrite")
	}
	if again.Characters["Elira"] != newRef {
		t.Error("re-import should dedupe to the same local card")
	}

	// Not-a-bundle inputs are rejected up front.
	if _, err := w.WorldsImport(ctx, ctrlproto.WorldImportParams{Bytes: []byte(`{"spec":"nope"}`)}); err == nil {
		t.Error("a wrong spec should be rejected")
	}
	if _, err := w.WorldsImport(ctx, ctrlproto.WorldImportParams{}); err == nil {
		t.Error("no bytes and no path should be rejected")
	}
}

// worlds.update (W5b): sessionless metadata edits — rename + description keep
// the id and Created (grouping survives), the cover sets/removes, and
// set+remove together is refused.
func TestWorldUpdateMetadata(t *testing.T) {
	w := draftWorkspace(t)
	ctx := context.Background()
	store := build.NewWorldStore()
	doc, err := store.Save(build.WorldDoc{Name: "Lowtown", Description: "grim"})
	if err != nil {
		t.Fatal(err)
	}

	view, err := w.WorldUpdate(ctx, ctrlproto.WorldUpdateParams{ID: doc.ID, Name: "Lowtown at Dusk", Description: "grimmer"})
	if err != nil {
		t.Fatal(err)
	}
	if view.ID != doc.ID || view.Name != "Lowtown at Dusk" || view.Description != "grimmer" {
		t.Fatalf("update = %+v", view)
	}
	after, err := store.Get(doc.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !after.Created.Equal(doc.Created) {
		t.Errorf("Created must survive an update: %v != %v", after.Created, doc.Created)
	}

	// Name "" keeps; description is applied verbatim ("" clears).
	view, err = w.WorldUpdate(ctx, ctrlproto.WorldUpdateParams{ID: doc.ID})
	if err != nil {
		t.Fatal(err)
	}
	if view.Name != "Lowtown at Dusk" || view.Description != "" {
		t.Fatalf("empty-name update = %+v", view)
	}

	view, err = w.WorldUpdate(ctx, ctrlproto.WorldUpdateParams{ID: doc.ID, Cover: []byte("png-bytes")})
	if err != nil {
		t.Fatal(err)
	}
	if view.CoverURL != "/media/worlds/"+doc.ID {
		t.Errorf("cover url = %q", view.CoverURL)
	}
	if _, err := w.WorldUpdate(ctx, ctrlproto.WorldUpdateParams{ID: doc.ID, Cover: []byte("x"), RemoveCover: true}); err == nil {
		t.Error("set+remove together should be refused")
	}
	view, err = w.WorldUpdate(ctx, ctrlproto.WorldUpdateParams{ID: doc.ID, RemoveCover: true})
	if err != nil {
		t.Fatal(err)
	}
	if view.CoverURL != "" {
		t.Errorf("cover should be gone, got %q", view.CoverURL)
	}
	if _, err := w.WorldUpdate(ctx, ctrlproto.WorldUpdateParams{ID: "no-such-world"}); err == nil {
		t.Error("updating a missing World should 404")
	}
}

// worlds.set_character_model (B): the World-scoped per-character default model.
// A pin lands in the doc and on the wire, an empty provider+model clears it, an
// off-roster name is refused, and a missing World 404s. The pin is what a new
// session in this World seeds its cast route from (workspace.go create path).
func TestWorldSetCharacterModel(t *testing.T) {
	w := draftWorkspace(t)
	ctx := context.Background()
	store := build.NewWorldStore()
	doc, err := store.Save(build.WorldDoc{Name: "Lowtown", Characters: map[string]string{"Elira": "elira-ref"}})
	if err != nil {
		t.Fatal(err)
	}

	view, err := w.WorldSetCharacterModel(ctx, ctrlproto.WorldSetCharacterModelParams{ID: doc.ID, Character: "Elira", Provider: "openai", Model: "gpt-5"})
	if err != nil {
		t.Fatal(err)
	}
	if got := view.CharacterModels["Elira"]; got.Provider != "openai" || got.Model != "gpt-5" {
		t.Fatalf("pin not on the wire view: %+v", view.CharacterModels)
	}
	if after, _ := store.Get(doc.ID); after.CharacterModels["Elira"].Model != "gpt-5" {
		t.Errorf("pin did not persist to the doc: %+v", after.CharacterModels)
	}

	// Empty provider AND model clears the pin — the character inherits again.
	view, err = w.WorldSetCharacterModel(ctx, ctrlproto.WorldSetCharacterModelParams{ID: doc.ID, Character: "Elira"})
	if err != nil {
		t.Fatal(err)
	}
	if _, still := view.CharacterModels["Elira"]; still {
		t.Errorf("empty provider+model should clear the pin, got %+v", view.CharacterModels)
	}

	// An off-roster name is refused (a pin without its member is meaningless).
	if _, err := w.WorldSetCharacterModel(ctx, ctrlproto.WorldSetCharacterModelParams{ID: doc.ID, Character: "Ghost", Model: "gpt-5"}); err == nil {
		t.Error("a character not on the roster should be refused")
	}
	// A missing World 404s.
	if _, err := w.WorldSetCharacterModel(ctx, ctrlproto.WorldSetCharacterModelParams{ID: "no-such-world", Character: "Elira", Model: "gpt-5"}); err == nil {
		t.Error("setting a model on a missing World should 404")
	}
}

// The defect the W5 live play-test found, fixed in W6: creating a PLAY session
// inside a saved World must warm its actors from the roster's library cards
// (the refs once resolved only as personas and creation failed outright). And
// the actor_spawn lore seam serves each actor exactly the entries they are
// cleared for — the same L2 filter chat's voiced lines use.
func TestPlayInWorldWarmsCardActors(t *testing.T) {
	w := draftWorkspace(t)
	ctx := context.Background()
	elira, err := w.CardsImport(ctx, ctrlproto.CardImportParams{Bytes: []byte(`{"name":"Elira","first_mes":"hi"}`)})
	if err != nil {
		t.Fatal(err)
	}
	rook, err := w.CardsImport(ctx, ctrlproto.CardImportParams{Bytes: []byte(`{"name":"Rook","first_mes":"Trouble?"}`)})
	if err != nil {
		t.Fatal(err)
	}
	doc, err := build.NewWorldStore().Save(build.WorldDoc{
		Name:       "Lowtown",
		Characters: map[string]string{"Elira": elira.ID, "Rook": rook.ID},
		Lore: []core.WorldLoreEntry{
			{Name: "curfew", Constant: true, Content: "The city is under curfew after dark."},
			{Name: "informant", Constant: true, Content: "Rook is the guild's informant.", Audience: []string{"Rook"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	info, err := w.CreateSession(ctx, ctrlproto.CreateOpts{Experience: "play", World: doc.ID})
	if err != nil {
		t.Fatalf("play-in-World create: %v", err)
	}
	live := w.live(info.ID)
	if len(live.actorCast) != 2 {
		t.Fatalf("both roster characters should warm as actors, got %d", len(live.actorCast))
	}
	for name, m := range live.actorCast {
		if m.Card == "" || !strings.HasSuffix(m.Card, "card.json") {
			t.Errorf("actor %s should be card-backed from the library, got %+v", name, m)
		}
	}

	// The L2 seam actor_spawn's WorldLore closure rides (same body): Rook sees
	// the shared entry AND his secret; Elira sees only the shared entry.
	rookBlock := live.worldLoreBlock("night falls over the quarter", "Rook")
	if !strings.Contains(rookBlock, "curfew") || !strings.Contains(rookBlock, "informant") {
		t.Errorf("Rook's block should carry shared + his secret: %q", rookBlock)
	}
	eliraBlock := live.worldLoreBlock("night falls over the quarter", "Elira")
	if !strings.Contains(eliraBlock, "curfew") || strings.Contains(eliraBlock, "informant") {
		t.Errorf("Elira's block must exclude Rook's secret: %q", eliraBlock)
	}
}
