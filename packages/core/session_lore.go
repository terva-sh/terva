package core

import (
	"reflect"
	"time"
)

// The World lorebook is stored as its own append-only rows rather than as a
// field on the last-wins meta row.
//
// It used to ride SessionMeta, which meant every meta writer re-serialized it:
// a background change, an author's-note edit, a model switch, a version stamp.
// Measured across 142 real session files, 60 meta rows carried a lorebook and
// 35 of them — 147 KB of 221 KB — carried it byte-for-byte UNCHANGED, written
// by a setter that had nothing to do with lore. The remaining 25 rows were real
// edits, but 21 of those changed exactly ONE entry and rewrote the whole book to
// say so, costing 3.2x the bytes that actually changed.
//
// So the shape is two changes, not one: the book leaves the meta row (kills the
// duplication), and an edit writes the ENTRY rather than the book (kills the
// amplification). The ops below are what the lore API already is — world.lore.put
// upserts by name, world.lore.delete removes by name — so the file reads as the
// history of the lorebook, the same way the meta rows read as the history of the
// model.
//
// The rows live in the session file, NOT in a sidecar file. The sidecar
// precedent (LogError's <session>.errors.jsonl) costs 13 non-test touchpoints:
// it must be gzipped and un-gzipped alongside its transcript, excluded from the
// session scanner by name so it isn't mistaken for one, and carried by every
// path that moves a session. A row in the file inherits all of that from the
// transcript for free — archive gzips the file, export streams the rows, delete
// removes the file — which matters here because losing session state in a path
// that forgot to carry it is the exact bug the export fix (#470) just closed.
const recordLore = "lore"

// Lore op values. A "set" replaces the whole book (the honest answer when an
// edit isn't expressible as upserts, e.g. a reorder); "put" upserts one entry by
// name, keeping its position if it already exists; "delete" removes one by name.
const (
	LoreOpSet    = "set"
	LoreOpPut    = "put"
	LoreOpDelete = "delete"
)

// sessionLore rides a "lore" row: one mutation of the session's World lorebook,
// folded in file order by the loader. Only the field its op uses is written.
type sessionLore struct {
	Op      string           `json:"op"`
	Entry   *WorldLoreEntry  `json:"entry,omitempty"`   // put
	Name    string           `json:"name,omitempty"`    // delete
	Entries []WorldLoreEntry `json:"entries,omitempty"` // set
}

// applyLoreOp folds one op into book and returns the result. Unknown ops are
// ignored, matching the loader's best-effort stance everywhere else: a row a
// future build wrote must not erase the book this build can still read.
//
// The returned slice never aliases book, so a caller holding the previous
// version keeps it intact.
func applyLoreOp(book []WorldLoreEntry, op sessionLore) []WorldLoreEntry {
	switch op.Op {
	case LoreOpSet:
		return cloneLore(op.Entries)
	case LoreOpPut:
		if op.Entry == nil {
			return book
		}
		out := cloneLore(book)
		for i := range out {
			if out[i].Name == op.Entry.Name {
				out[i] = *op.Entry
				return out
			}
		}
		return append(out, *op.Entry)
	case LoreOpDelete:
		out := make([]WorldLoreEntry, 0, len(book))
		for _, e := range book {
			if e.Name != op.Name {
				out = append(out, e)
			}
		}
		if len(out) == 0 {
			return nil
		}
		return out
	}
	return book
}

// foldMetaLore applies a meta row's contribution to a folded lorebook.
//
// Below v4 the row IS the book, so an absent one means cleared. At v4 and up the
// book lives in lore rows and the meta row has nothing to say about it — reading
// its absence as a clear would erase the lorebook on the next background change.
//
// Shared by every reader that folds a book (the loader, the branch), because two
// hand-written copies of this rule is how one of them ends up wrong.
func foldMetaLore(book []WorldLoreEntry, m SessionMeta) []WorldLoreEntry {
	if m.FormatVersion < sessionFormatVersionLore {
		return m.WorldLore
	}
	return book
}

// applyLoreOps folds a sequence of ops in order.
func applyLoreOps(book []WorldLoreEntry, ops []sessionLore) []WorldLoreEntry {
	for _, op := range ops {
		book = applyLoreOp(book, op)
	}
	return book
}

// loreOps returns the ops that turn prev into next, or nil when one wholesale
// "set" is the honest answer.
//
// It never guesses. The ops it builds are APPLIED to prev and the result
// compared against next; anything that doesn't reconstruct the book exactly —
// a reorder, a book with duplicate names, a case-only rename — returns nil and
// the caller writes a set. Incremental storage is an optimization, so it is
// allowed to decline, and declining is always correct. That is what makes the
// diff safe to trust with a lorebook full of secrets: a wrong answer degrades
// to the old whole-book row rather than to a lost entry.
//
// An empty (non-nil) slice means prev and next are already equal — write
// nothing at all, mirroring UpdateModel's no-op.
func loreOps(prev, next []WorldLoreEntry) []sessionLore {
	if len(next) == 0 {
		// Clearing the book is a set of nothing; there is no smaller way to say it.
		return nil
	}
	if duplicateLoreNames(prev) || duplicateLoreNames(next) {
		return nil // upsert-by-name is ambiguous against a duplicate
	}
	inNext := make(map[string]bool, len(next))
	for _, e := range next {
		inNext[e.Name] = true
	}
	prevAt := make(map[string]int, len(prev))
	for i, e := range prev {
		prevAt[e.Name] = i
	}

	ops := []sessionLore{}
	for _, e := range prev {
		if !inNext[e.Name] {
			ops = append(ops, sessionLore{Op: LoreOpDelete, Name: e.Name})
		}
	}
	puts := 0
	for i := range next {
		if j, ok := prevAt[next[i].Name]; ok && reflect.DeepEqual(prev[j], next[i]) {
			continue
		}
		entry := next[i] // copy: the op outlives the caller's backing array
		ops = append(ops, sessionLore{Op: LoreOpPut, Entry: &entry})
		puts++
	}
	// Only puts carry an entry's bytes; a delete is a name. So the question is
	// whether the puts already amount to the whole book — the first write of a
	// book, or a wholesale replacement — in which case one set says the same
	// thing in one row. Counting all ops instead would push a book that merely
	// LOST entries into a full rewrite, which is backwards.
	if puts >= len(next) {
		return nil
	}
	if !reflect.DeepEqual(applyLoreOps(cloneLore(prev), ops), cloneLore(next)) {
		return nil // the ops don't reconstruct the book — don't write them
	}
	return ops
}

// duplicateLoreNames reports whether two entries share a name, which makes
// upsert-by-name ambiguous. Real books can't hold one (world.lore.put upserts),
// but a hand-imported bundle can.
func duplicateLoreNames(book []WorldLoreEntry) bool {
	seen := make(map[string]bool, len(book))
	for _, e := range book {
		if seen[e.Name] {
			return true
		}
		seen[e.Name] = true
	}
	return false
}

// cloneLore copies a book so the session's persisted snapshot and the caller's
// slice can't alias. Returns nil for an empty book, so a cleared book and a
// never-set one are the same value.
func cloneLore(book []WorldLoreEntry) []WorldLoreEntry {
	if len(book) == 0 {
		return nil
	}
	return append([]WorldLoreEntry(nil), book...)
}

// SetWorldLore replaces (or, with a nil/empty slice, clears) the session's
// World lore. Like SetNote it is durable across a restart, editable any number
// of times, and a per-turn-tail input rather than a cached-prefix one, so a
// change takes effect next turn without a rebuild. A no-op on a nil receiver.
//
// The book is persisted as "lore" rows, not on the meta row — see recordLore for
// why — and the write is the DIFF against what is already on disk, so editing
// one entry of a ten-entry book appends that entry. A call that changes nothing
// writes nothing.
//
// The API stays wholesale on purpose: every caller already computes the whole
// book (the world tools rebuild the slice to add one entry), so the incremental
// storage is entirely below this line and no call site has to know about it.
func (s *Session) SetWorldLore(entries []WorldLoreEntry) error {
	if s == nil {
		return nil
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	ops := loreOps(s.persistedLore, entries)
	if ops == nil {
		ops = []sessionLore{{Op: LoreOpSet, Entries: entries}}
	}
	if len(ops) == 0 {
		s.Meta.WorldLore = entries // in memory only; disk already says this
		return nil
	}
	// Declare the format BEFORE writing the rows it explains, so a reader that
	// stops at any row has never seen a lore row without the version that says
	// lore rows exist. The bump also flips writeMeta to stop carrying the book,
	// which is why it cannot happen after the fact.
	if s.Meta.FormatVersion < sessionFormatVersionLore {
		s.Meta.FormatVersion = sessionFormatVersionLore
		if err := s.writeMetaLocked(); err != nil {
			return err
		}
	}
	now := time.Now().UTC()
	for i := range ops {
		if err := s.writeLineLocked(sessionLine{Type: recordLore, Lore: &ops[i], At: &now}); err != nil {
			return err
		}
	}
	s.Meta.WorldLore = entries
	s.persistedLore = cloneLore(entries)
	return nil
}
