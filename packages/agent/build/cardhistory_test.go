package build

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/card"
	"terva.sh/terva/packages/testsupport"
)

// cardWith builds a minimal CCv2 document whose greeting is the given text, so a
// test can make a card differ by one field.
func cardWith(name, greeting string) string {
	return fmt.Sprintf(`{"spec":"chara_card_v2","spec_version":"2.0","data":{"name":%q,"first_mes":%q}}`, name, greeting)
}

func TestCardHistorySnapshotsTheOutgoingCard(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	st := NewCardStore()

	imported, err := st.ImportBytes([]byte(cardWith("Mara", "one")))
	if err != nil {
		t.Fatal(err)
	}
	if vs, err := st.history().List(imported.ID, nil); err != nil || len(vs) != 0 {
		t.Fatalf("an unedited card has no history: %v, %d", err, len(vs))
	}

	// The first edit records the card exactly as it was imported — no special
	// case at import, and a library that predates this feature gets covered the
	// moment it is touched.
	first, err := st.Edit(imported.ID, []byte(cardWith("Mara", "two")))
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Warnings) != 0 {
		t.Errorf("a healthy snapshot warns about nothing: %v", first.Warnings)
	}
	vs, err := st.history().List(imported.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(vs) != 1 {
		t.Fatalf("one edit records one revision, got %d", len(vs))
	}
	raw, err := st.history().Get(imported.ID, vs[0].Ref)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != string(imported.Raw) {
		t.Errorf("the snapshot must be the OUTGOING card\n got %s\nwant %s", raw, imported.Raw)
	}
	if vs[0].Name != "Mara" || vs[0].Bytes != len(imported.Raw) || vs[0].Saved.IsZero() {
		t.Errorf("revision metadata is wrong: %+v", vs[0])
	}

	// The second edit records the first edit's result, newest first.
	if _, err := st.Edit(imported.ID, []byte(cardWith("Mara", "three"))); err != nil {
		t.Fatal(err)
	}
	vs, err = st.history().List(imported.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(vs) != 2 {
		t.Fatalf("two edits record two revisions, got %d", len(vs))
	}
	newest, err := st.history().Get(imported.ID, vs[0].Ref)
	if err != nil {
		t.Fatal(err)
	}
	if string(newest) != string(first.Raw) {
		t.Errorf("newest revision should be the previous edit\n got %s\nwant %s", newest, first.Raw)
	}
}

func TestCardHistorySkipsAnUnchangedSave(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	st := NewCardStore()

	sc, err := st.ImportBytes([]byte(cardWith("Mara", "one")))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Edit(sc.ID, []byte(cardWith("Mara", "two"))); err != nil {
		t.Fatal(err)
	}
	// Saving the same content again must not burn a retention slot.
	for i := 0; i < 3; i++ {
		if _, err := st.Edit(sc.ID, []byte(cardWith("Mara", "two"))); err != nil {
			t.Fatal(err)
		}
	}
	vs, err := st.history().List(sc.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(vs) != 1 {
		t.Fatalf("a no-op save records nothing, got %d revisions", len(vs))
	}
}

// Snapshot is independently idempotent at the head of the log. Edit already
// refuses to record a no-op save, so this guards the other route in: an edit
// whose snapshot succeeded but whose write then failed leaves prev on disk
// already recorded, and the retry must not store it twice.
func TestCardHistorySnapshotIsIdempotentAtTheHead(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	h := NewCardHistoryStore()

	body := []byte(cardWith("Mara", "one"))
	for i := 0; i < 3; i++ {
		if err := h.Snapshot("mara-000000000000", body, nil); err != nil {
			t.Fatal(err)
		}
	}
	vs, err := h.List("mara-000000000000", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(vs) != 1 {
		t.Fatalf("re-snapshotting the head records nothing new, got %d", len(vs))
	}
	// A genuinely different state still lands.
	if err := h.Snapshot("mara-000000000000", []byte(cardWith("Mara", "two")), nil); err != nil {
		t.Fatal(err)
	}
	if vs, err := h.List("mara-000000000000", nil); err != nil || len(vs) != 2 {
		t.Fatalf("a changed state must be recorded: %v, %d", err, len(vs))
	}
}

// An empty history is a normal state (nothing edited yet), not an error, and
// nothing to snapshot is a no-op rather than an empty revision.
func TestCardHistoryEmptyCases(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	h := NewCardHistoryStore()

	if vs, err := h.List("mara-000000000000", nil); err != nil || len(vs) != 0 {
		t.Fatalf("unedited card: %v, %d", err, len(vs))
	}
	if err := h.Snapshot("mara-000000000000", nil, nil); err != nil {
		t.Fatalf("snapshotting nothing: %v", err)
	}
	if vs, err := h.List("mara-000000000000", nil); err != nil || len(vs) != 0 {
		t.Fatalf("an empty snapshot records nothing: %v, %d", err, len(vs))
	}
	if err := h.Forget("mara-000000000000"); err != nil {
		t.Errorf("forgetting an empty history: %v", err)
	}
	// A bad card id is refused everywhere rather than reaching the filesystem.
	if err := h.Snapshot("../escape", []byte("{}"), nil); err == nil {
		t.Error("Snapshot must reject an invalid card id")
	}
	if _, err := h.List("../escape", nil); err == nil {
		t.Error("List must reject an invalid card id")
	}
	if err := h.Forget("../escape"); err == nil {
		t.Error("Forget must reject an invalid card id")
	}
}

func TestCardHistoryPrunesButAlwaysKeepsTheOldest(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	st := NewCardStore()

	imported, err := st.ImportBytes([]byte(cardWith("Mara", "v0")))
	if err != nil {
		t.Fatal(err)
	}
	// Comfortably past the budget, and fast enough to exercise the
	// same-millisecond ref collision path.
	for i := 1; i <= cardHistoryKeep+5; i++ {
		if _, err := st.Edit(imported.ID, []byte(cardWith("Mara", fmt.Sprintf("v%d", i)))); err != nil {
			t.Fatal(err)
		}
	}
	vs, err := st.history().List(imported.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(vs) != cardHistoryKeep {
		t.Fatalf("retention is %d revisions, got %d", cardHistoryKeep, len(vs))
	}
	// The as-imported card is the one revision worth most after a run of
	// machine edits, so it outlives the budget.
	oldest, err := st.history().Get(imported.ID, vs[len(vs)-1].Ref)
	if err != nil {
		t.Fatal(err)
	}
	if string(oldest) != string(imported.Raw) {
		t.Errorf("the oldest revision must survive pruning\n got %s\nwant %s", oldest, imported.Raw)
	}
	// …and the rest of the window is the most RECENT changes, not the earliest.
	second, err := st.history().Get(imported.ID, vs[0].Ref)
	if err != nil {
		t.Fatal(err)
	}
	if want := cardWith("Mara", fmt.Sprintf("v%d", cardHistoryKeep+4)); !jsonHasGreeting(t, second, fmt.Sprintf("v%d", cardHistoryKeep+4)) {
		t.Errorf("newest retained revision should be the last superseded edit\n got %s\nwant greeting from %s", second, want)
	}
	// Refs stay unique even when several edits land in one millisecond.
	seen := map[string]bool{}
	for _, v := range vs {
		if seen[v.Ref] {
			t.Fatalf("duplicate revision ref %q", v.Ref)
		}
		seen[v.Ref] = true
	}
}

func TestCardHistoryRestoreIsItselfRecorded(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	st := NewCardStore()

	imported, err := st.ImportBytes([]byte(cardWith("Mara", "original")))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Edit(imported.ID, []byte(cardWith("Mara", "ruined by the doctor"))); err != nil {
		t.Fatal(err)
	}
	vs, err := st.history().List(imported.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	old, err := st.history().Get(imported.ID, vs[0].Ref)
	if err != nil {
		t.Fatal(err)
	}
	// A restore is an ordinary Edit, which is what makes it undoable in turn.
	restored, err := st.Edit(imported.ID, old)
	if err != nil {
		t.Fatal(err)
	}
	if string(restored.Raw) != string(imported.Raw) {
		t.Errorf("restore did not round-trip\n got %s\nwant %s", restored.Raw, imported.Raw)
	}
	vs, err = st.history().List(imported.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(vs) != 2 {
		t.Fatalf("a restore records what it replaced, want 2 revisions, got %d", len(vs))
	}
	if !jsonHasGreeting(t, mustGetRevision(t, st, imported.ID, vs[0].Ref), "ruined by the doctor") {
		t.Error("the newest revision should be the state the restore replaced")
	}
}

func TestCardHistoryIsPerCard(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	st := NewCardStore()

	a, err := st.ImportBytes([]byte(cardWith("Mara", "a")))
	if err != nil {
		t.Fatal(err)
	}
	b, err := st.ImportBytes([]byte(cardWith("Kobeni", "b")))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Edit(a.ID, []byte(cardWith("Mara", "a2"))); err != nil {
		t.Fatal(err)
	}
	if vs, err := st.history().List(b.ID, nil); err != nil || len(vs) != 0 {
		t.Fatalf("editing one card must not touch another's history: %v, %d", err, len(vs))
	}
}

func TestCardHistoryRejectsATraversalRef(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	st := NewCardStore()

	sc, err := st.ImportBytes([]byte(cardWith("Mara", "one")))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Edit(sc.ID, []byte(cardWith("Mara", "two"))); err != nil {
		t.Fatal(err)
	}
	// A READABLE file just outside the card's history directory. Without it this
	// test passes for the wrong reason — any escaping path errors merely because
	// nothing happens to be there — and would not notice validation being
	// removed entirely. The bait makes refusal the only way to pass.
	bait := filepath.Join(CardHistoryDir(), "bait.json")
	if err := os.WriteFile(bait, []byte(`{"name":"stolen"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, ref := range []string{"", "..", "../bait", "../../cards", "1/../../bait", "abc", "1234.json", "1e9"} {
		if raw, err := st.history().Get(sc.ID, ref); err == nil {
			t.Errorf("ref %q must be rejected, read %s", ref, raw)
		}
	}
}

func TestCardDeleteForgetsHistory(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	st := NewCardStore()

	sc, err := st.ImportBytes([]byte(cardWith("Mara", "one")))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Edit(sc.ID, []byte(cardWith("Mara", "two"))); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(CardHistoryDir(), sc.ID)
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("history should exist before the delete: %v", err)
	}
	if err := st.Delete(sc.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("a deleted card keeps no prose on disk, stat = %v", err)
	}
	if vs, err := st.history().List(sc.ID, nil); err != nil || len(vs) != 0 {
		t.Errorf("history after delete: %v, %d", err, len(vs))
	}
}

// The sibling-root decision, asserted rather than assumed. StoredCard.Added is
// the CARD DIRECTORY's mtime and powers the "recently added" sort, which only
// works because nothing is created inside that directory after import — so a
// history stored under cards/<id>/ would silently restamp every card you edit.
func TestCardEditLeavesAddedAndTheCardDirAlone(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	st := NewCardStore()

	sc, err := st.ImportBytes([]byte(cardWith("Mara", "one")))
	if err != nil {
		t.Fatal(err)
	}
	before, err := st.Get(sc.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Edit(sc.ID, []byte(cardWith("Mara", "two"))); err != nil {
		t.Fatal(err)
	}
	after, err := st.Get(sc.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !after.Added.Equal(before.Added) {
		t.Errorf("editing a card moved its Added stamp: %v → %v", before.Added, after.Added)
	}
	entries, err := os.ReadDir(filepath.Join(CardsDir(), sc.ID))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	if len(names) != 1 || names[0] != cardJSONName {
		t.Errorf("the card dir must hold only the card and its avatar, got %v", names)
	}
}

func mustGetRevision(t *testing.T, st *CardStore, id, ref string) []byte {
	t.Helper()
	raw, err := st.history().Get(id, ref)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// jsonHasGreeting reports whether a snapshot's first_mes is the given text,
// without depending on how card.Marshal orders or wraps fields.
func jsonHasGreeting(t *testing.T, raw []byte, want string) bool {
	t.Helper()
	c, err := card.ParseJSON(raw)
	if err != nil {
		t.Fatalf("snapshot does not parse: %v", err)
	}
	return c.FirstMes == want
}

// pngWith builds a card PNG whose embedded card is `body` and whose FILE bytes
// differ by `tail` — two portraits of the same character.
func pngWith(body, tail string) []byte {
	return append(mkCharaPNG(body), []byte(tail)...)
}

const kobeniV1 = `{"spec":"chara_card_v2","spec_version":"2.0","data":{"name":"Kobeni","first_mes":"as imported"}}`

// The bug this whole change is about. An import's id is derived from the card's
// CONTENT, so re-importing a file you have since edited resolves to the same
// directory — and used to overwrite card.json with no record and no warning,
// silently reverting every edit. Import does not go through Edit, so the
// revision log did not see it either.
func TestReimportOverAnEditedCardIsRecordedAndAnnounced(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	st := NewCardStore()

	imported, err := st.ImportBytes(pngWith(kobeniV1, "PIXELS-A"))
	if err != nil {
		t.Fatal(err)
	}
	edited := `{"spec":"chara_card_v2","spec_version":"2.0","data":{"name":"Kobeni","first_mes":"MY EDIT"}}`
	if _, err := st.Edit(imported.ID, []byte(edited)); err != nil {
		t.Fatal(err)
	}

	// Re-import the original file.
	again, err := st.ImportBytes(pngWith(kobeniV1, "PIXELS-A"))
	if err != nil {
		t.Fatal(err)
	}
	if again.ID != imported.ID {
		t.Fatalf("re-import should land on the same card: %s vs %s", again.ID, imported.ID)
	}
	// It still reverts — that is the chosen behaviour, import stays idempotent —
	// but it now SAYS so instead of doing it silently.
	if len(again.Warnings) == 0 {
		t.Fatal("replacing a stored card must not be silent")
	}
	if !strings.Contains(strings.Join(again.Warnings, " "), "already in your library") {
		t.Errorf("warning does not say what happened: %v", again.Warnings)
	}

	// …and the edit it displaced is recoverable, which is the whole point.
	vs, err := st.history().List(imported.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(vs) != 2 {
		t.Fatalf("want 2 revisions (the import, then the edit it replaced), got %d", len(vs))
	}
	if !jsonHasGreeting(t, mustGetRevision(t, st, imported.ID, vs[0].Ref), "MY EDIT") {
		t.Error("the newest revision must be the edit the re-import displaced")
	}
	back, err := st.RestoreRevision(imported.ID, vs[0].Ref)
	if err != nil {
		t.Fatal(err)
	}
	if back.Card.FirstMes != "MY EDIT" {
		t.Errorf("restoring gave back %q, want the edit", back.Card.FirstMes)
	}
}

// The original complaint: same card data, different picture. The data-only
// dedupe would have swallowed this — nothing in card.json changed.
func TestReimportReplacingOnlyThePortraitIsRecorded(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	st := NewCardStore()

	first, err := st.ImportBytes(pngWith(kobeniV1, "PIXELS-A"))
	if err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(filepath.Join(CardsDir(), first.ID, cardAvatarName))
	if err != nil {
		t.Fatal(err)
	}

	again, err := st.ImportBytes(pngWith(kobeniV1, "PIXELS-B-LONGER"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(again.Warnings, " "), "portrait") {
		t.Errorf("a replaced portrait must be announced: %v", again.Warnings)
	}
	vs, err := st.history().List(first.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(vs) != 1 {
		t.Fatalf("a picture-only replacement is still a revision, got %d", len(vs))
	}
	kept, ok, err := st.history().AvatarAsOf(first.ID, vs[0].Ref)
	if err != nil || !ok {
		t.Fatalf("the displaced portrait must be retained: ok=%v err=%v", ok, err)
	}
	if !bytes.Equal(kept, original) {
		t.Error("the retained portrait is not the one that was replaced")
	}

	// Restoring brings the picture back with the data.
	if _, err := st.RestoreRevision(first.ID, vs[0].Ref); err != nil {
		t.Fatal(err)
	}
	now, err := os.ReadFile(filepath.Join(CardsDir(), first.ID, cardAvatarName))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(now, original) {
		t.Error("restore must put the portrait back too")
	}
}

// The point-in-time rule. A .png is written only when that write CHANGED the
// picture, so the portrait in force at a revision is the earliest one stored at
// or after it — not "the one attached to this revision", which most do not have.
func TestAvatarAsOfResolvesThroughLaterRevisions(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	st := NewCardStore()

	sc, err := st.ImportBytes(pngWith(kobeniV1, "PIXELS-A"))
	if err != nil {
		t.Fatal(err)
	}
	avatarA, _ := os.ReadFile(filepath.Join(CardsDir(), sc.ID, cardAvatarName))

	// R1: a data-only edit. The picture does not move, so no .png is stored.
	if _, err := st.Edit(sc.ID, []byte(`{"spec":"chara_card_v2","spec_version":"2.0","data":{"name":"Kobeni","first_mes":"v1"}}`)); err != nil {
		t.Fatal(err)
	}
	// R2: a re-import that swaps the picture. It must carry the body that MINTED
	// the id — the edited body hashes to a different id and would land on a new
	// card rather than colliding, which is the narrow window this bug lives in.
	if _, err := st.ImportBytes(pngWith(kobeniV1, "PIXELS-B-LONGER")); err != nil {
		t.Fatal(err)
	}
	vs, err := st.history().List(sc.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(vs) != 2 {
		t.Fatalf("want 2 revisions, got %d", len(vs))
	}
	oldest := vs[len(vs)-1].Ref // the as-imported card, recorded by the edit

	// The oldest revision stored no picture of its own, but the card DID look
	// like portrait A back then — the next revision to record one says so.
	got, ok, err := st.history().AvatarAsOf(sc.ID, oldest)
	if err != nil || !ok {
		t.Fatalf("expected a portrait for the oldest revision: ok=%v err=%v", ok, err)
	}
	if !bytes.Equal(got, avatarA) {
		t.Error("AvatarAsOf resolved to the wrong picture")
	}

	// Restoring it brings back both halves of that moment.
	if _, err := st.RestoreRevision(sc.ID, oldest); err != nil {
		t.Fatal(err)
	}
	now, _ := os.ReadFile(filepath.Join(CardsDir(), sc.ID, cardAvatarName))
	if !bytes.Equal(now, avatarA) {
		t.Error("restoring an early revision must restore the picture in force then")
	}
}

func TestPortraitFlagAndQuietPaths(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	st := NewCardStore()

	// A FIRST import replaces nothing, so it neither records nor warns.
	sc, err := st.ImportBytes(pngWith(kobeniV1, "PIXELS-A"))
	if err != nil {
		t.Fatal(err)
	}
	if len(sc.Warnings) != 0 {
		t.Errorf("a fresh import has nothing to announce: %v", sc.Warnings)
	}
	if vs, _ := st.history().List(sc.ID, nil); len(vs) != 0 {
		t.Errorf("a fresh import records nothing, got %d", len(vs))
	}

	// An ordinary edit is aimed at a card by id — landing on it is the point,
	// not a surprise, so it gets no "replaced" warning.
	ed, err := st.Edit(sc.ID, []byte(`{"spec":"chara_card_v2","spec_version":"2.0","data":{"name":"Kobeni","first_mes":"v1"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(ed.Warnings) != 0 {
		t.Errorf("an edit must not warn about replacing: %v", ed.Warnings)
	}

	// That revision did not move the picture, so restoring it would not either.
	curAvatar, _ := os.ReadFile(filepath.Join(CardsDir(), sc.ID, cardAvatarName))
	vs, err := st.history().List(sc.ID, curAvatar)
	if err != nil {
		t.Fatal(err)
	}
	if len(vs) != 1 || vs[0].Portrait {
		t.Errorf("a data-only revision must not claim the portrait moved: %+v", vs)
	}

	// After a picture swap it does.
	if _, err := st.ImportBytes(pngWith(kobeniV1, "PIXELS-B-LONGER")); err != nil {
		t.Fatal(err)
	}
	curAvatar, _ = os.ReadFile(filepath.Join(CardsDir(), sc.ID, cardAvatarName))
	vs, err = st.history().List(sc.ID, curAvatar)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range vs {
		if !v.Portrait {
			t.Errorf("every revision predating the swap should report the portrait moved: %+v", v)
		}
	}
}

// A pruned revision takes its portrait with it. Leaving the .png behind would
// hand a picture to some older revision it never belonged to, via AvatarAsOf.
func TestPruningDropsTheCompanionPortrait(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	st := NewCardStore()

	sc, err := st.ImportBytes(pngWith(kobeniV1, "PIXELS-A"))
	if err != nil {
		t.Fatal(err)
	}
	// One edit FIRST, so the picture-bearing revision is not the oldest one —
	// the oldest is never pruned, so putting the portrait there would make this
	// test unable to fail.
	if _, err := st.Edit(sc.ID, []byte(`{"spec":"chara_card_v2","spec_version":"2.0","data":{"name":"Kobeni","first_mes":"before the swap"}}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ImportBytes(pngWith(kobeniV1, "PIXELS-B-LONGER")); err != nil {
		t.Fatal(err)
	}
	if pngs, _ := filepath.Glob(filepath.Join(CardHistoryDir(), sc.ID, "*.png")); len(pngs) != 1 {
		t.Fatalf("setup: expected exactly one stored portrait, got %d", len(pngs))
	}
	for i := 0; i < cardHistoryKeep+4; i++ {
		body := fmt.Sprintf(`{"spec":"chara_card_v2","spec_version":"2.0","data":{"name":"Kobeni","first_mes":"v%d"}}`, i)
		if _, err := st.Edit(sc.ID, []byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := os.ReadDir(filepath.Join(CardHistoryDir(), sc.ID))
	if err != nil {
		t.Fatal(err)
	}
	refs := map[string]bool{}
	var pngs []string
	for _, e := range entries {
		name := e.Name()
		if strings.HasSuffix(name, ".json") {
			refs[strings.TrimSuffix(name, ".json")] = true
		} else if strings.HasSuffix(name, ".png") {
			pngs = append(pngs, strings.TrimSuffix(name, ".png"))
		}
	}
	for _, ref := range pngs {
		if !refs[ref] {
			t.Errorf("orphaned portrait %s.png with no revision to belong to", ref)
		}
	}
}

// Two picture-only re-imports in a row. The card's data never moves, so the
// head-of-log check sees the same JSON both times — if it judged only the data,
// the second swap would be treated as already recorded and the middle portrait
// would be dropped on the floor.
func TestConsecutivePortraitSwapsAreEachRecorded(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	st := NewCardStore()

	sc, err := st.ImportBytes(pngWith(kobeniV1, "PIXELS-A"))
	if err != nil {
		t.Fatal(err)
	}
	avatarA, _ := os.ReadFile(filepath.Join(CardsDir(), sc.ID, cardAvatarName))
	if _, err := st.ImportBytes(pngWith(kobeniV1, "PIXELS-BB")); err != nil {
		t.Fatal(err)
	}
	avatarB, _ := os.ReadFile(filepath.Join(CardsDir(), sc.ID, cardAvatarName))
	if _, err := st.ImportBytes(pngWith(kobeniV1, "PIXELS-CCC")); err != nil {
		t.Fatal(err)
	}

	vs, err := st.history().List(sc.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(vs) != 2 {
		t.Fatalf("two portrait swaps are two revisions, got %d", len(vs))
	}
	newest, ok, err := st.history().AvatarAsOf(sc.ID, vs[0].Ref)
	if err != nil || !ok || !bytes.Equal(newest, avatarB) {
		t.Errorf("the newest revision should hold the portrait it replaced (B)")
	}
	oldest, ok, err := st.history().AvatarAsOf(sc.ID, vs[1].Ref)
	if err != nil || !ok || !bytes.Equal(oldest, avatarA) {
		t.Errorf("the older revision should still hold portrait A")
	}
}
