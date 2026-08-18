package build

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"

	"terva.sh/terva/packages/agent/card"
	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/agent/slug"

	"terva.sh/terva/packages/privfs"
)

// A card's edit history is terva-owned metadata about a card, so — like a card
// group (groupstore.go) or a card's default model (cardmodelstore.go) — it lives
// OUTSIDE the card directory, keyed by the library card id:
//
//	$TERVA_HOME/card-history/<cardid>/<unixmilli>.json
//
// Two reasons it is a sibling root rather than cards/<id>/history/:
//
//   - StoredCard.Added is the CARD DIRECTORY's mtime, and the whole reason that
//     works is that nothing is ever created inside the directory after import
//     (an in-place rewrite of card.json leaves the parent's mtime alone). A
//     history subdirectory would stamp it on the first edit, silently turning
//     "added" into "last edited" and reshuffling the library's recency sort.
//   - a card you export or share must not carry a record of your own drafts.
//
// WHY it exists: hand-editing is recoverable because you remember what you
// wrote. The card doctor and enrich rewrite fields you did NOT type — and the
// doctor can propose removals — so the state you'd want back is one you may
// never have read. That is why the snapshot is taken inside CardStore.Edit, the
// one choke point every mutator funnels through, rather than in the editor UI:
// a machine-driven apply path cannot skip it by forgetting to ask.
//
// The snapshot captures the OUTGOING bytes — the state being left. Two things
// fall out of that choice for free: the first edit of any card records it
// exactly as imported (no special case at import, no migration for a library
// that already exists), and a restore, being itself an Edit, records what it
// replaced — so a restore can be undone.

// CardHistoryDir is the on-disk root, created lazily on the first snapshot; a
// missing directory reads as "nothing has been edited yet".
func CardHistoryDir() string { return filepath.Join(config.TervaHome(), "card-history") }

// cardHistoryKeep is how many revisions a card retains. The OLDEST is never
// pruned (see prune): a straight newest-N would drop the as-imported card after
// N edits, which is the single revision most worth having after a run of
// enrich/doctor passes — the loop this store exists for. So a card keeps its
// original plus the most recent cardHistoryKeep-1 changes.
const cardHistoryKeep = 10

// CardVersion is one retained revision of a card: an opaque ref to fetch or
// restore it by, when it was superseded, its size, and the card's name AT THAT
// REVISION (parsed from the snapshot, so a list still reads correctly after a
// rename rather than labelling every row with the current name).
type CardVersion struct {
	Ref   string
	Saved time.Time
	Bytes int
	Name  string
	// Card is the revision itself, already parsed. List has to parse a snapshot
	// to read its name, so handing the result out costs nothing and spares a
	// caller that wants to compare revisions from re-reading every file.
	// Zero if the snapshot did not parse.
	Card card.Card
	// Portrait is whether restoring this revision would also change the card's
	// picture — see AvatarAsOf for why that is not simply "has a .png".
	Portrait bool
}

// CardHistoryStore is the per-card revision log rooted at CardHistoryDir().
type CardHistoryStore struct{ dir string }

// NewCardHistoryStore opens the log at the current $TERVA_HOME.
func NewCardHistoryStore() *CardHistoryStore { return &CardHistoryStore{dir: CardHistoryDir()} }

// Snapshot records prev — the bytes a card is about to stop having — and prunes
// the card back to its retention budget.
//
// prevAvatar is the portrait being replaced, or nil when the portrait is not
// changing. It is stored as a companion <ref>.png rather than as its own kind
// of revision: a portrait only ever changes as part of a write to the card, so
// one ref names both halves of that moment. Most revisions have no .png at all,
// because editing card data never touches the picture.
//
// Two no-ops that are deliberately not errors: empty prev (nothing to record),
// and a state already at the head of the log — measured over BOTH halves, so a
// re-import that changes only the pixels is still recorded.
func (s *CardHistoryStore) Snapshot(cardID string, prev, prevAvatar []byte) error {
	if err := slug.ValidID(cardID); err != nil {
		return err
	}
	if len(prev) == 0 {
		return nil
	}
	dir := filepath.Join(s.dir, cardID)
	refs, err := s.refs(cardID)
	if err != nil {
		return err
	}
	if n := len(refs); n > 0 {
		newest, err := os.ReadFile(filepath.Join(dir, refs[n-1]+".json"))
		headAvatar, _ := os.ReadFile(filepath.Join(dir, refs[n-1]+".png"))
		if err == nil && string(newest) == string(prev) && bytes.Equal(headAvatar, prevAvatar) {
			return nil
		}
	}
	if err := privfs.MkdirAll(dir); err != nil {
		return err
	}
	// Unix millis both names and orders the revision, so a ref must be strictly
	// greater than every ref already present — NOT merely the current time.
	// Several edits inside one millisecond issue future-dated refs (T, T+1, …),
	// and pruning then frees low ones; a snapshot that trusted the clock alone
	// would drop into a freed hole, sort as the oldest revision, and be pruned
	// away next time while genuinely old ones looked newest.
	ms := time.Now().UnixMilli()
	if n := len(refs); n > 0 {
		if last, err := strconv.ParseInt(refs[n-1], 10, 64); err == nil && last >= ms {
			ms = last + 1
		}
	}
	for {
		path := filepath.Join(dir, strconv.FormatInt(ms, 10)+".json")
		if _, err := os.Stat(path); os.IsNotExist(err) {
			if err := privfs.WriteFileMode(path, prev, 0o644); err != nil {
				return err
			}
			if len(prevAvatar) > 0 {
				if err := privfs.WriteFileMode(path[:len(path)-len(".json")]+".png", prevAvatar, 0o644); err != nil {
					return err
				}
			}
			break
		}
		ms++
	}
	return s.prune(cardID)
}

// List returns a card's retained revisions, newest first. A card that has never
// been edited reads as an empty list, not an error.
//
// currentAvatar is the card's portrait as it stands, used to answer whether
// restoring each revision would also change the picture. Passing it in (rather
// than reading it here) keeps the comparison exact: "this revision has a stored
// .png" is very nearly the same answer, but not when a portrait was replaced
// and later brought back.
func (s *CardHistoryStore) List(cardID string, currentAvatar []byte) ([]CardVersion, error) {
	refs, err := s.refs(cardID)
	if err != nil {
		return nil, err
	}
	// One backward pass gives every revision the portrait in force at it: the
	// earliest stored .png at or after it (see AvatarAsOf for why that is the
	// rule). Carrying it backwards costs one pass instead of a scan per row.
	govern := make([]string, len(refs))
	nearest := ""
	for i := len(refs) - 1; i >= 0; i-- {
		if _, err := os.Stat(filepath.Join(s.dir, cardID, refs[i]+".png")); err == nil {
			nearest = refs[i]
		}
		govern[i] = nearest
	}
	// Almost every card has at most one stored portrait, so this cache turns the
	// whole list into a single image read.
	moved := map[string]bool{}
	portraitMoved := func(ref string) bool {
		if ref == "" {
			return false
		}
		if v, ok := moved[ref]; ok {
			return v
		}
		raw, err := os.ReadFile(filepath.Join(s.dir, cardID, ref+".png"))
		v := err == nil && !bytes.Equal(raw, currentAvatar)
		moved[ref] = v
		return v
	}

	out := make([]CardVersion, 0, len(refs))
	for i := len(refs) - 1; i >= 0; i-- {
		ref := refs[i]
		raw, err := os.ReadFile(filepath.Join(s.dir, cardID, ref+".json"))
		if err != nil {
			continue
		}
		v := CardVersion{Ref: ref, Bytes: len(raw), Portrait: portraitMoved(govern[i])}
		if ms, err := strconv.ParseInt(ref, 10, 64); err == nil {
			v.Saved = time.UnixMilli(ms)
		}
		// A snapshot that will not parse is still restorable bytes, so it is
		// listed — just without a name to label it by.
		if c, err := card.ParseJSON(raw); err == nil {
			v.Name = c.Name
			v.Card = c
		}
		out = append(out, v)
	}
	return out, nil
}

// LastEdited reports when this card's content last CHANGED, and whether it ever
// has. A card with no history has never been edited since it was imported, which
// the caller reads as "modified when it was added".
//
// It opens nothing. refs reads directory entry NAMES and the timestamp IS the
// name, so the whole answer comes from one ReadDir of a small directory — which
// matters because the library list calls this once per card, and for most cards
// the directory does not exist at all and ReadDir fails straight through.
//
// The newest ref is the right answer because Snapshot runs at the moment of an
// edit, and ONLY when something actually changed: CardStore.write skips it on a
// byte-identical save so an idle re-save cannot consume a retention slot. That
// makes this strictly better than card.json's mtime for "modified" — write
// rewrites card.json unconditionally, so its mtime records the last write
// ATTEMPT rather than the last change.
func (s *CardHistoryStore) LastEdited(cardID string) (time.Time, bool) {
	refs, err := s.refs(cardID)
	if err != nil || len(refs) == 0 {
		return time.Time{}, false
	}
	// refs sorts ascending, so the last one is the most recent edit. Note that a
	// ref is an ORDERING token first and a clock reading second: Snapshot issues
	// strictly-increasing refs, so a burst of edits inside one millisecond is
	// future-dated by a millisecond or two. Ordering — the only thing a sort key
	// needs — is exact; treat the value as "when", not as an audit timestamp.
	ms, err := strconv.ParseInt(refs[len(refs)-1], 10, 64)
	if err != nil {
		return time.Time{}, false
	}
	return time.UnixMilli(ms), true
}

// Get loads one retained revision's card JSON.
func (s *CardHistoryStore) Get(cardID, ref string) ([]byte, error) {
	if err := slug.ValidID(cardID); err != nil {
		return nil, err
	}
	if err := validVersionRef(ref); err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(filepath.Join(s.dir, cardID, ref+".json"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("card %q has no revision %q", cardID, ref)
		}
		return nil, err
	}
	return raw, nil
}

// AvatarAsOf returns the portrait the card had at the given revision, and
// whether it differs from what the card has now.
//
// The rule follows from when a .png is written at all — only when that write
// CHANGED the picture. So the portrait in force at revision R is the one stored
// by the earliest revision at or after R that recorded one; if no later
// revision recorded one, the picture has not moved since and the card's current
// avatar is already correct.
func (s *CardHistoryStore) AvatarAsOf(cardID, ref string) ([]byte, bool, error) {
	if err := slug.ValidID(cardID); err != nil {
		return nil, false, err
	}
	if err := validVersionRef(ref); err != nil {
		return nil, false, err
	}
	refs, err := s.refs(cardID)
	if err != nil {
		return nil, false, err
	}
	want, err := strconv.ParseInt(ref, 10, 64)
	if err != nil {
		return nil, false, fmt.Errorf("invalid revision %q", ref)
	}
	for _, r := range refs { // oldest first
		n, err := strconv.ParseInt(r, 10, 64)
		if err != nil || n < want {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(s.dir, cardID, r+".png"))
		if err == nil {
			return raw, true, nil
		}
	}
	return nil, false, nil
}

// Forget drops a card's whole history. Called when the card itself is deleted:
// unlike a group's stale member (a dangling reference, filtered on read), a
// stray history is the character's own prose, and leaving it on disk after a
// delete is a surprise rather than a convenience.
func (s *CardHistoryStore) Forget(cardID string) error {
	if err := slug.ValidID(cardID); err != nil {
		return err
	}
	if err := os.RemoveAll(filepath.Join(s.dir, cardID)); err != nil {
		return err
	}
	return nil
}

// refs lists a card's revision refs, oldest first. A missing directory is an
// empty history.
func (s *CardHistoryStore) refs(cardID string) ([]string, error) {
	if err := slug.ValidID(cardID); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(filepath.Join(s.dir, cardID))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var refs []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if filepath.Ext(name) != ".json" {
			continue
		}
		ref := name[:len(name)-len(".json")]
		if validVersionRef(ref) != nil {
			continue
		}
		refs = append(refs, ref)
	}
	// Refs are fixed-meaning integers, so sort them numerically rather than
	// lexically — 13 digits today, 14 in the year 2286.
	sort.Slice(refs, func(i, j int) bool {
		a, _ := strconv.ParseInt(refs[i], 10, 64)
		b, _ := strconv.ParseInt(refs[j], 10, 64)
		return a < b
	})
	return refs, nil
}

// prune trims a card to cardHistoryKeep revisions, always sparing the oldest.
func (s *CardHistoryStore) prune(cardID string) error {
	refs, err := s.refs(cardID)
	if err != nil {
		return err
	}
	n := len(refs)
	if n <= cardHistoryKeep {
		return nil
	}
	// Keep refs[0] (the card as it was before its first edit) plus the newest
	// cardHistoryKeep-1; everything between them goes.
	for _, ref := range refs[1 : n-(cardHistoryKeep-1)] {
		if err := os.Remove(filepath.Join(s.dir, cardID, ref+".json")); err != nil && !os.IsNotExist(err) {
			return err
		}
		// The companion portrait goes with its revision. Dropping the .json and
		// keeping the .png would leave a picture that AvatarAsOf would then hand
		// to some older revision it never belonged to.
		if err := os.Remove(filepath.Join(s.dir, cardID, ref+".png")); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

// validVersionRef rejects anything that is not a plain revision stamp. Refs are
// unix-milli integers, so digits-only is both the natural check and a complete
// answer to path traversal — no separator, dot, or ".." can survive it.
func validVersionRef(ref string) error {
	if ref == "" || len(ref) > 20 {
		return fmt.Errorf("invalid revision %q", ref)
	}
	for _, r := range ref {
		if r < '0' || r > '9' {
			return fmt.Errorf("invalid revision %q", ref)
		}
	}
	return nil
}
