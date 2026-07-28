package core

import (
	"bufio"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

// loreRows reads back every lore row in a session file, in order.
func loreRows(t *testing.T, path string) []sessionLore {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer f.Close()

	var out []sessionLore
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 8<<20)
	for sc.Scan() {
		var row struct {
			Type string      `json:"type"`
			Lore sessionLore `json:"lore"`
		}
		if err := json.Unmarshal(sc.Bytes(), &row); err != nil || row.Type != recordLore {
			continue
		}
		out = append(out, row.Lore)
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scan %s: %v", path, err)
	}
	return out
}

// rawMetaRows returns each meta row as raw JSON, so a test can ask what is
// literally on the row rather than what the struct decodes to.
func rawMetaRows(t *testing.T, path string) []string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var out []string
	for _, line := range strings.Split(string(b), "\n") {
		if line == "" {
			continue
		}
		var head sessionLineHead
		if json.Unmarshal([]byte(line), &head) == nil && head.Type == "meta" {
			out = append(out, line)
		}
	}
	return out
}

// book builds a lorebook of n entries whose content is large enough that
// duplicating it is visible in the file size — which is the whole point.
func book(n int) []WorldLoreEntry {
	out := make([]WorldLoreEntry, 0, n)
	for i := range n {
		out = append(out, WorldLoreEntry{
			Name:    fmt.Sprintf("entry-%d", i),
			Keys:    []string{fmt.Sprintf("key-%d", i)},
			Content: strings.Repeat(fmt.Sprintf("lore body %d. ", i), 30),
		})
	}
	return out
}

// newLoreSession returns a saved, reopenable session with one message in it
// (Close prunes a session that never held any).
func newLoreSession(t *testing.T) *Session {
	t.Helper()
	dir := testsupport.TempDir(t)
	s, err := NewSession(dir, dir, "anthropic", "claude-opus-5", "0.126.19")
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	seed(t, s)
	return s
}

// TestALoreEditWritesTheEntryNotTheBook is the measurement this change exists
// for. Across 142 real session files, 21 of the 25 real lore edits changed
// exactly ONE entry and rewrote the entire book to say so — 3.2x the bytes that
// actually changed. An edit must now cost about what it changed.
func TestALoreEditWritesTheEntryNotTheBook(t *testing.T) {
	s := newLoreSession(t)
	defer s.Close()

	full := book(10)
	if err := s.SetWorldLore(full); err != nil {
		t.Fatalf("SetWorldLore: %v", err)
	}
	bookBytes := len(mustJSON(t, full))

	before := fileSize(t, s.Path)
	edited := cloneLore(full)
	edited[4].Content = strings.Repeat("the fourth entry, rewritten. ", 15)
	if err := s.SetWorldLore(edited); err != nil {
		t.Fatalf("SetWorldLore (edit): %v", err)
	}
	grew := fileSize(t, s.Path) - before

	entryBytes := int64(len(mustJSON(t, edited[4])))
	if grew > entryBytes*2 {
		t.Errorf("editing one entry grew the file by %d B; the entry is %d B — the write is not incremental", grew, entryBytes)
	}
	if grew >= int64(bookBytes) {
		t.Errorf("editing one entry grew the file by %d B, the whole book is %d B — that is the old behaviour", grew, bookBytes)
	}

	rows := loreRows(t, s.Path)
	if len(rows) != 2 {
		t.Fatalf("got %d lore rows, want 2 (initial set, one put): %+v", len(rows), rows)
	}
	if rows[1].Op != LoreOpPut || rows[1].Entry == nil || rows[1].Entry.Name != "entry-4" {
		t.Errorf("second row = %+v, want a put of entry-4", rows[1])
	}
}

// TestAMetaWriteNoLongerCarriesTheBook is the other half of the measurement:
// 35 of 60 lore-bearing meta rows carried the book byte-for-byte unchanged,
// written by a setter with nothing to do with lore.
func TestAMetaWriteNoLongerCarriesTheBook(t *testing.T) {
	s := newLoreSession(t)
	if err := s.SetWorldLore(book(6)); err != nil {
		t.Fatalf("SetWorldLore: %v", err)
	}
	before := fileSize(t, s.Path)

	// Four writes that have nothing to do with lore. Each used to re-serialize
	// the whole lorebook.
	for _, step := range []func() error{
		func() error { return s.SetNote("keep it tense") },
		func() error { return s.SetBackground("bg_rain") },
		func() error { return s.UpdateModel("openai-codex", "gpt-5.6-sol") },
		func() error { return s.SetCoordination("off") },
	} {
		if err := step(); err != nil {
			t.Fatalf("meta write: %v", err)
		}
	}
	grew := fileSize(t, s.Path) - before
	bookBytes := int64(len(mustJSON(t, book(6))))
	if grew >= bookBytes {
		t.Errorf("four non-lore meta writes grew the file by %d B, one copy of the book is %d B — the book is still riding the meta row", grew, bookBytes)
	}
	for i, raw := range rawMetaRows(t, s.Path) {
		if strings.Contains(raw, `"world_lore"`) {
			t.Errorf("meta row %d still carries world_lore: %s", i, raw)
		}
	}
	path := s.Path
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// And the book is still there.
	reopened, _, err := OpenSession(path)
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	defer reopened.Close()
	if !reflect.DeepEqual(reopened.Meta.WorldLore, book(6)) {
		t.Errorf("reopened book = %d entries, want 6", len(reopened.Meta.WorldLore))
	}
	if reopened.Meta.Note != "keep it tense" || reopened.Meta.Background != "bg_rain" {
		t.Errorf("the other meta fields did not survive: note=%q bg=%q", reopened.Meta.Note, reopened.Meta.Background)
	}
}

// TestALegacyBookSurvivesAMetaWriteThatIsNotALoreEdit is the migration hazard,
// and the reason writeMeta strips the book only at v4.
//
// A session written before the lore rows keeps its book on the meta rows
// forever — an append-only file is never rewritten. If this build stripped the
// field unconditionally, the next background change would append a meta row
// with no world_lore, and the loader would read that absence as an empty book:
// the lorebook erased by a write that never mentioned it.
func TestALegacyBookSurvivesAMetaWriteThatIsNotALoreEdit(t *testing.T) {
	legacy := book(4)
	path := writeLegacySession(t, sessionFormatVersionAmend, legacy)

	s, _, err := OpenSession(path)
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	if !reflect.DeepEqual(s.Meta.WorldLore, legacy) {
		t.Fatalf("legacy book did not load: got %d entries, want %d", len(s.Meta.WorldLore), len(legacy))
	}
	if err := s.SetBackground("bg_rain"); err != nil {
		t.Fatalf("SetBackground: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	reopened, _, err := OpenSession(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer reopened.Close()
	if !reflect.DeepEqual(reopened.Meta.WorldLore, legacy) {
		t.Errorf("after a background change the legacy book is %d entries, want %d — a meta write erased it",
			len(reopened.Meta.WorldLore), len(legacy))
	}
}

// TestALegacyBookMigratesOnTheFirstLoreEdit: read-old / write-new, lazily. No
// file is ever rewritten; a legacy session moves to the rows the first time its
// lore actually changes.
func TestALegacyBookMigratesOnTheFirstLoreEdit(t *testing.T) {
	legacy := book(4)
	path := writeLegacySession(t, sessionFormatVersionAmend, legacy)

	s, _, err := OpenSession(path)
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	next := cloneLore(legacy)
	next[1].Content = "rewritten"
	if err := s.SetWorldLore(next); err != nil {
		t.Fatalf("SetWorldLore: %v", err)
	}
	if got := s.Meta.FormatVersion; got != sessionFormatVersionLore {
		t.Errorf("format version = %d after the first lore edit, want %d", got, sessionFormatVersionLore)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	rows := loreRows(t, path)
	if len(rows) != 1 || rows[0].Op != LoreOpPut {
		t.Fatalf("got %+v, want one put — the diff is against the legacy book, not a fresh set", rows)
	}
	reopened, _, err := OpenSession(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer reopened.Close()
	if !reflect.DeepEqual(reopened.Meta.WorldLore, next) {
		t.Errorf("migrated book = %+v, want %+v", reopened.Meta.WorldLore, next)
	}
	// The version bump must be visible to an older build, which is the whole
	// point of declaring it: it skips the lore rows and would otherwise present
	// a session whose secrets silently vanished.
	if warn := reopened.LoadWarnings; len(warn) != 0 {
		t.Errorf("this build must read v%d cleanly, got warnings: %v", sessionFormatVersionLore, warn)
	}
}

// TestASessionThatNeverHoldsLoreNeverDeclaresTheFormat — the amend precedent.
// A coding session has no book, so it must not claim a version that makes an
// older build warn about it.
func TestASessionThatNeverHoldsLoreNeverDeclaresTheFormat(t *testing.T) {
	s := newLoreSession(t)
	defer s.Close()
	if err := s.SetNote("a note"); err != nil {
		t.Fatalf("SetNote: %v", err)
	}
	for i, m := range metaRows(t, s.Path) {
		if m.FormatVersion >= sessionFormatVersionLore {
			t.Errorf("meta row %d declares v%d without holding any lore", i, m.FormatVersion)
		}
	}
}

// TestSettingTheSameBookWritesNothing mirrors UpdateModel's no-op. The world
// tools rebuild and re-persist the whole slice routinely; a re-save that changed
// nothing must not append.
func TestSettingTheSameBookWritesNothing(t *testing.T) {
	s := newLoreSession(t)
	defer s.Close()
	if err := s.SetWorldLore(book(3)); err != nil {
		t.Fatalf("SetWorldLore: %v", err)
	}
	before := fileSize(t, s.Path)
	for range 3 {
		if err := s.SetWorldLore(book(3)); err != nil {
			t.Fatalf("SetWorldLore (repeat): %v", err)
		}
	}
	if got := fileSize(t, s.Path); got != before {
		t.Errorf("three identical re-saves grew the file by %d B, want 0", got-before)
	}
}

// TestClearingTheBookSurvivesAReopen — "" is a value, not an absence.
func TestClearingTheBookSurvivesAReopen(t *testing.T) {
	s := newLoreSession(t)
	if err := s.SetWorldLore(book(3)); err != nil {
		t.Fatalf("SetWorldLore: %v", err)
	}
	if err := s.SetWorldLore(nil); err != nil {
		t.Fatalf("clear: %v", err)
	}
	path := s.Path
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	reopened, _, err := OpenSession(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer reopened.Close()
	if len(reopened.Meta.WorldLore) != 0 {
		t.Errorf("cleared book came back with %d entries", len(reopened.Meta.WorldLore))
	}
}

// TestADeleteIsDurable — the op that removes rather than replaces.
func TestADeleteIsDurable(t *testing.T) {
	s := newLoreSession(t)
	if err := s.SetWorldLore(book(4)); err != nil {
		t.Fatalf("SetWorldLore: %v", err)
	}
	kept := append(cloneLore(book(4)[:1]), book(4)[2:]...) // drop entry-1
	if err := s.SetWorldLore(kept); err != nil {
		t.Fatalf("SetWorldLore (delete): %v", err)
	}
	rows := loreRows(t, s.Path)
	if len(rows) != 2 || rows[1].Op != LoreOpDelete || rows[1].Name != "entry-1" {
		t.Fatalf("got %+v, want a delete of entry-1", rows)
	}
	path := s.Path
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	reopened, _, err := OpenSession(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer reopened.Close()
	if !reflect.DeepEqual(reopened.Meta.WorldLore, kept) {
		t.Errorf("after a delete the book is %d entries, want %d", len(reopened.Meta.WorldLore), len(kept))
	}
}

// TestAReorderFallsBackToAWholeBookRow: the diff declines rather than guessing.
// Order is meaningful — the per-turn tail renders entries in it — and upsert-by-
// name cannot express a move, so the honest answer is a set.
func TestAReorderFallsBackToAWholeBookRow(t *testing.T) {
	s := newLoreSession(t)
	defer s.Close()
	orig := book(3)
	if err := s.SetWorldLore(orig); err != nil {
		t.Fatalf("SetWorldLore: %v", err)
	}
	reversed := []WorldLoreEntry{orig[2], orig[1], orig[0]}
	if err := s.SetWorldLore(reversed); err != nil {
		t.Fatalf("SetWorldLore (reorder): %v", err)
	}
	rows := loreRows(t, s.Path)
	if len(rows) != 2 || rows[1].Op != LoreOpSet {
		t.Fatalf("got %+v, want the reorder to fall back to a set", rows)
	}
	if !reflect.DeepEqual(rows[1].Entries, reversed) {
		t.Errorf("the set row does not carry the reordered book")
	}
}

// TestLoreOpsReconstructTheBookOrDecline is the property the whole design rests
// on: whatever loreOps returns, applying it to the previous book yields the
// target exactly — or it returns nil and the caller writes the book wholesale.
// There is no third outcome, which is what makes an incremental lorebook safe
// to trust with entries that are secrets.
func TestLoreOpsReconstructTheBookOrDecline(t *testing.T) {
	rng := rand.New(rand.NewSource(20260728))
	entry := func(name string) WorldLoreEntry {
		return WorldLoreEntry{
			Name:     name,
			Content:  fmt.Sprintf("body-%d", rng.Intn(1000)),
			Constant: rng.Intn(2) == 0,
			Keys:     []string{fmt.Sprintf("k%d", rng.Intn(3))},
		}
	}
	randBook := func(n int) []WorldLoreEntry {
		out := make([]WorldLoreEntry, 0, n)
		for i := range n {
			out = append(out, entry(fmt.Sprintf("e%d", i)))
		}
		return out
	}

	// The mutations a lorebook actually undergoes, not random book pairs. The
	// measurement says which ones matter: 21 of 25 real edits changed exactly one
	// entry. incremental marks the shapes the diff MUST handle — if one of those
	// starts declining, the change stops paying for itself and nothing else
	// would notice.
	mutations := []struct {
		name        string
		incremental bool
		apply       func(b []WorldLoreEntry) []WorldLoreEntry
	}{
		{"edit one entry", true, func(b []WorldLoreEntry) []WorldLoreEntry {
			out := cloneLore(b)
			out[rng.Intn(len(out))].Content = fmt.Sprintf("rewritten-%d", rng.Intn(1000))
			return out
		}},
		{"append one entry", true, func(b []WorldLoreEntry) []WorldLoreEntry {
			return append(cloneLore(b), entry(fmt.Sprintf("new-%d", rng.Intn(1000))))
		}},
		{"delete one entry", true, func(b []WorldLoreEntry) []WorldLoreEntry {
			i := rng.Intn(len(b))
			return append(cloneLore(b[:i]), b[i+1:]...)
		}},
		{"reorder", false, func(b []WorldLoreEntry) []WorldLoreEntry {
			out := cloneLore(b)
			rng.Shuffle(len(out), func(i, j int) { out[i], out[j] = out[j], out[i] })
			return out
		}},
		{"replace wholesale", false, func(b []WorldLoreEntry) []WorldLoreEntry {
			return randBook(len(b))
		}},
		{"clear", false, func(b []WorldLoreEntry) []WorldLoreEntry { return nil }},
	}

	declines := map[string]int{}
	for i := range 3000 {
		m := mutations[rng.Intn(len(mutations))]
		prev := randBook(1 + rng.Intn(9))
		next := m.apply(prev)

		ops := loreOps(prev, next)
		if ops == nil {
			// Below two entries a put and a set are the same one row, so declining
			// costs nothing and the incremental assertion below doesn't apply.
			if len(next) >= 2 {
				declines[m.name]++
			}
			continue
		}
		got := applyLoreOps(cloneLore(prev), ops)
		if !reflect.DeepEqual(cloneLore(got), cloneLore(next)) {
			t.Fatalf("case %d (%s): ops did not reconstruct the book\nprev: %+v\nnext: %+v\nops:  %+v\ngot:  %+v",
				i, m.name, prev, next, ops, got)
		}
	}
	for _, m := range mutations {
		if m.incremental && declines[m.name] > 0 {
			t.Errorf("%q declined %d times — the shape the measurement says is the common case is not being written incrementally",
				m.name, declines[m.name])
		}
	}
	if declines["reorder"]+declines["clear"] == 0 {
		t.Error("nothing declined — the fallback path is untested by this run")
	}
}

// TestAnUnknownLoreOpLeavesTheBookAlone: a row from a future build must not
// erase what this build can still read. Matches the loader's stance everywhere.
func TestAnUnknownLoreOpLeavesTheBookAlone(t *testing.T) {
	b := book(2)
	if got := applyLoreOp(b, sessionLore{Op: "rename"}); !reflect.DeepEqual(got, b) {
		t.Errorf("an unknown op changed the book: %+v", got)
	}
}

// TestExportImportCarriesTheLoreRows — the path that lost fourteen fields once
// already. Lore rows are non-meta rows, so they stream; this pins that they do.
func TestExportImportCarriesTheLoreRows(t *testing.T) {
	s := newLoreSession(t)
	orig := book(3)
	if err := s.SetWorldLore(orig); err != nil {
		t.Fatalf("SetWorldLore: %v", err)
	}
	edited := cloneLore(orig)
	edited[0].Content = "changed after export was designed"
	if err := s.SetWorldLore(edited); err != nil {
		t.Fatalf("SetWorldLore (edit): %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	dir := testsupport.TempDir(t)
	bundle, err := ExportSession(s.Path, filepath.Join(dir, "out.tervasession"))
	if err != nil {
		t.Fatalf("ExportSession: %v", err)
	}
	dst := testsupport.TempDir(t)
	imported, err := ImportSession(bundle, dst, dst, "0.126.19")
	if err != nil {
		t.Fatalf("ImportSession: %v", err)
	}
	round, _, err := OpenSession(imported)
	if err != nil {
		t.Fatalf("OpenSession(imported): %v", err)
	}
	defer round.Close()
	if !reflect.DeepEqual(round.Meta.WorldLore, edited) {
		t.Errorf("round-tripped book = %+v, want %+v", round.Meta.WorldLore, edited)
	}
}

// TestExportImportCarriesALegacyBook is the reason SessionMeta keeps the
// world_lore json tag. Export re-marshals every meta row through the struct, so
// dropping the tag would silently strip the lorebook out of every session
// written before v4 — the same shape of loss the export fix just closed.
func TestExportImportCarriesALegacyBook(t *testing.T) {
	legacy := book(3)
	path := writeLegacySession(t, sessionFormatVersionAmend, legacy)

	dir := testsupport.TempDir(t)
	bundle, err := ExportSession(path, filepath.Join(dir, "out.tervasession"))
	if err != nil {
		t.Fatalf("ExportSession: %v", err)
	}
	dst := testsupport.TempDir(t)
	imported, err := ImportSession(bundle, dst, dst, "0.126.19")
	if err != nil {
		t.Fatalf("ImportSession: %v", err)
	}
	round, _, err := OpenSession(imported)
	if err != nil {
		t.Fatalf("OpenSession(imported): %v", err)
	}
	defer round.Close()
	if !reflect.DeepEqual(round.Meta.WorldLore, legacy) {
		t.Errorf("a legacy book did not survive export/import: got %d entries, want %d",
			len(round.Meta.WorldLore), len(legacy))
	}
}

// ---- helpers ----

// writeLegacySession hand-writes a session file in the pre-lore-row form: the
// book on a meta row, at the given format version. Tests can't produce one with
// this build, which is the point — the migration path has no other witness.
func writeLegacySession(t *testing.T, formatVersion int, lore []WorldLoreEntry) string {
	t.Helper()
	dir := testsupport.TempDir(t)
	path := filepath.Join(dir, "20260101-000000-legacy1.jsonl")

	meta := SessionMeta{
		ID:            "legacy-session",
		CWD:           dir,
		Model:         "claude-opus-5",
		Provider:      "anthropic",
		Started:       time.Now().UTC().Add(-time.Hour),
		Version:       "0.126.0",
		FormatVersion: formatVersion,
		Experience:    "play",
		WorldLore:     lore,
	}
	w := encodeWireMessage(provider.Message{
		Role:    provider.RoleUser,
		Content: []provider.Content{provider.TextBlock{Text: "hello"}},
		Time:    time.Now().UTC(),
	})
	var b strings.Builder
	b.Write(mustJSON(t, sessionLine{Type: "meta", Meta: &meta}))
	b.WriteByte('\n')
	b.Write(mustJSON(t, sessionLine{Type: "message", Message: &w}))
	b.WriteByte('\n')
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatalf("write legacy session: %v", err)
	}
	return path
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

func fileSize(t *testing.T, path string) int64 {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return fi.Size()
}
