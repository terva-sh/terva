package build

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"terva.sh/terva/packages/agent/card"
	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/agent/slug"
)

// The character-card library. Cards were path-only before Stage: --card pointed
// at a PNG/JSON on disk, card.Load parsed it into strings, and the pixels (the
// avatar) were dropped on the floor. The library gives cards a home so every
// controller — the Stage app, the panel, the TUI — can inspect, choose, edit,
// and spawn from the same set, and so an imported card KEEPS its avatar.
//
// Layout: $TERVA_HOME/cards/<id>/ holding
//   - card.json   the normalized card (card.Marshal — a CCv2 document that
//                 round-trips unknown `extensions` verbatim). Source of truth.
//   - avatar.png  the ORIGINAL PNG bytes, when imported from a PNG. This is the
//                 portrait; card.json is data. Absent for a JSON import.
//
// A card is data, never code (the authority rule in package card is unchanged):
// editing a card here never grants it any capability.

// CardsDir is the on-disk card library root, $TERVA_HOME/cards.
func CardsDir() string { return filepath.Join(config.TervaHome(), "cards") }

const (
	cardJSONName   = "card.json"
	cardAvatarName = "avatar.png"
)

// StoredCard is a card as it lives in the library: a stable id, the parsed card,
// its normalized JSON bytes, and whether an avatar image was retained.
type StoredCard struct {
	ID   string
	Card card.Card
	// Raw is the normalized card.json (card.Marshal output) — handed to clients
	// verbatim so an editor round-trips `extensions` it does not itself render.
	Raw       []byte
	AvatarExt string // "png" when an avatar was retained, else ""
	// Added is when the card entered the library — the card directory's mtime,
	// which is stamped when the dir is created at import and (unlike card.json's
	// mtime) is NOT disturbed by an in-place edit of the card, so it stays the
	// import time. Zero if the directory could not be stat'd. Powers the
	// "recently added" sort; no separate storage or migration.
	Added time.Time
	// Warnings are non-fatal notes from the import that a user should see — a
	// downscaled or dropped portrait, say. An imported card is usable with
	// warnings; they explain what the library did to it, so a surprise (a
	// missing avatar) has a stated reason instead of looking like a bug.
	Warnings []string
}

// HasAvatar reports whether an avatar image is stored alongside the card.
func (c StoredCard) HasAvatar() bool { return c.AvatarExt != "" }

// CardStore is the library rooted at CardsDir(). The directory is created lazily
// on the first import; a missing directory reads as an empty library.
type CardStore struct{ dir, historyDir string }

// NewCardStore opens the library at the current $TERVA_HOME. Callers that set
// TERVA_HOME (tests, project scope) must do so before constructing the store.
func NewCardStore() *CardStore {
	return &CardStore{dir: CardsDir(), historyDir: CardHistoryDir()}
}

// history is the revision log for this library. Resolved from the same
// $TERVA_HOME reading that fixed dir, so a store cannot end up writing cards to
// one home and their history to another.
func (s *CardStore) history() *CardHistoryStore { return &CardHistoryStore{dir: s.historyDir} }

// List returns every stored card, sorted by name then id. A card directory that
// fails to parse is skipped rather than failing the whole listing.
func (s *CardStore) List() ([]StoredCard, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []StoredCard
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		sc, err := s.Get(e.Name())
		if err != nil {
			continue
		}
		out = append(out, sc)
	}
	sort.Slice(out, func(i, j int) bool {
		ni, nj := strings.ToLower(out[i].Card.Name), strings.ToLower(out[j].Card.Name)
		if ni == nj {
			return out[i].ID < out[j].ID
		}
		return ni < nj
	})
	return out, nil
}

// Get loads one card by id.
func (s *CardStore) Get(id string) (StoredCard, error) {
	if err := slug.ValidID(id); err != nil {
		return StoredCard{}, err
	}
	dir := filepath.Join(s.dir, id)
	raw, err := os.ReadFile(filepath.Join(dir, cardJSONName))
	if err != nil {
		if os.IsNotExist(err) {
			return StoredCard{}, fmt.Errorf("card %q not found", id)
		}
		return StoredCard{}, err
	}
	c, err := card.ParseJSON(raw)
	if err != nil {
		return StoredCard{}, fmt.Errorf("card %q: %w", id, err)
	}
	sc := StoredCard{ID: id, Card: c, Raw: raw, AvatarExt: avatarExt(dir)}
	if fi, err := os.Stat(dir); err == nil {
		sc.Added = fi.ModTime()
	}
	return sc, nil
}

// ImportPath imports a card from a server-local .png or .json path.
func (s *CardStore) ImportPath(path string) (StoredCard, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return StoredCard{}, err
	}
	return s.ImportBytes(data)
}

// ImportBytes imports a card from raw file bytes (a PNG with an embedded card,
// or a bare JSON card). A PNG's pixels are kept as the avatar, downscaled first
// when the portrait is oversized (see normalizeAvatar). The id is derived from
// the card's content, so re-importing the same card is idempotent (same id, same
// directory) rather than piling up duplicates.
func (s *CardStore) ImportBytes(data []byte) (StoredCard, error) {
	c, err := card.LoadBytes(data)
	if err != nil {
		return StoredCard{}, err
	}
	raw, err := card.Marshal(c)
	if err != nil {
		return StoredCard{}, err
	}
	id := slug.ID(c.Name, raw)
	// A card imported before slugs folded diacritics is filed under the old
	// spelling ("zo-<hash>" for "Zoë"). The content-hash half is unchanged, so
	// point the import at that directory rather than minting a duplicate
	// beside it — re-import stays idempotent across the fold.
	if legacy := slug.IDFrom(slug.Legacy(c.Name), raw); legacy != id {
		if _, err := os.Stat(filepath.Join(s.dir, id)); os.IsNotExist(err) {
			if _, err := os.Stat(filepath.Join(s.dir, legacy)); err == nil {
				id = legacy
			}
		}
	}
	var warnings []string
	// More than one `chara` chunk means the parse had to choose. Say so: the
	// candidates are routinely different REVISIONS of the character rather than
	// duplicates, so the unchosen one is not equivalent, and a user comparing
	// this import against the same card elsewhere deserves to know why they
	// differ. See card.CountCharaChunks for why first-wins is the rule.
	if n := card.CountCharaChunks(data); n > 1 {
		warnings = append(warnings, fmt.Sprintf(
			"this PNG embeds %d character records; imported the first. They are often different revisions of the card, so check the description and greeting look right.", n))
	}
	// A V3 card's data now survives import and round-trips unharmed, but some of
	// what V3 can DECLARE is not yet something terva acts on. Say which, on
	// arrival, so a lorebook that never fires has a stated reason rather than
	// looking like a bug in the card.
	warnings = append(warnings, card.UnsupportedV3Features(c)...)
	var avatar []byte
	if card.IsPNG(data) {
		var note string
		avatar, note = normalizeAvatar(data)
		if note != "" {
			warnings = append(warnings, note)
		}
	}
	// noteReplacement: an import is the one write a person does not aim at a
	// specific card. Edit and restore name their target, so landing on it is the
	// point rather than a surprise worth a warning.
	sc, err := s.write(id, c, raw, avatar, true)
	if err != nil {
		return StoredCard{}, err
	}
	sc.Warnings = append(warnings, sc.Warnings...)
	return sc, nil
}

// Duplicate copies a stored card under a new name — portrait included — and
// files the copy as a card of its own, with its own empty revision history.
//
// It exists because both obvious ways to copy a card fail QUIETLY. Ids are
// content-addressed and ImportBytes is idempotent by content, so exporting a
// card and re-importing it returns the card you already had rather than a copy.
// And a copy assembled client-side from the card JSON does mint a new id, but
// leaves the portrait behind — the avatar lives outside the card document, so
// only a PNG import carries pixels.
//
// The rename is therefore not cosmetic: it is the whole reason the copy is a
// different card. Both checks below exist so that a rename which fails to do
// that job is REFUSED rather than written, because either write would land on a
// card that already exists and report success — the failure mode this method
// was added to remove, reintroduced one level up.
func (s *CardStore) Duplicate(id, name string) (StoredCard, error) {
	if err := slug.ValidID(id); err != nil {
		return StoredCard{}, err
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return StoredCard{}, fmt.Errorf("a duplicate needs a name")
	}
	src, err := s.Get(id)
	if err != nil {
		return StoredCard{}, err
	}
	// A shallow copy is enough because Name is the only field that changes;
	// nothing here reaches into the slices, the character book, or extensions,
	// which the copy shares by value with what Marshal is about to serialize.
	c := src.Card
	c.Name = name
	raw, err := card.Marshal(c)
	if err != nil {
		return StoredCard{}, err
	}
	dup := slug.ID(c.Name, raw)
	// These two refusals cover ONE unsafe write, not two: when the name is
	// unchanged the id is unchanged, so the card.json the collision check stats
	// is this card's own and it would refuse anyway. The first check exists for
	// what it SAYS. "Another card already holds these contents" is false when
	// the other card is the one you are copying, and it leaves the author
	// guessing at a fix that is simply "pick a different name".
	if dup == id {
		return StoredCard{}, fmt.Errorf("%q is the name this card already has — a duplicate needs a different one", name)
	}
	// A collision here means a DIFFERENT card with this name and these contents
	// is already filed: writing would snapshot it into history and overwrite it,
	// which is a merge wearing a copy's clothes.
	if _, err := os.Stat(filepath.Join(s.dir, dup, cardJSONName)); err == nil {
		return StoredCard{}, fmt.Errorf("a card named %q with these contents is already in your library", name)
	}
	// Read the portrait rather than re-deriving it: the stored avatar.png has
	// already been through normalizeAvatar, so the copy inherits the picture the
	// original actually shows, not a second downscale of it. A JSON-imported
	// card has none, and write treats empty bytes as "no portrait".
	avatar, _ := os.ReadFile(filepath.Join(s.dir, id, cardAvatarName))
	// noteReplacement false: the two checks above are what guarantee this id is
	// unwritten, so there is nothing displaced to warn about.
	return s.write(dup, c, raw, avatar, false)
}

// Fork writes an edited card document as a NEW card, leaving the original
// exactly as it was — the copy-on-write half of Duplicate.
//
// Duplicate answers "give me a second copy of this character to take in another
// direction", and so demands a new NAME: a copy under the same name with the
// same contents is the same card, and writing it would report success while
// creating nothing. Fork answers a different question — "apply this change here
// WITHOUT it reaching everywhere else" — where the name is usually the one thing
// that must not change. Kobeni in one World and Kobeni in another are both
// Kobeni; only their contents diverge.
//
// The id falls out of content-addressing, which is what makes this safe without
// any new bookkeeping:
//
//   - an edit that changes the bytes hashes to a new id, so the fork is a new
//     card and the original's card.json is never opened for writing;
//   - an edit that changes NOTHING hashes to the SAME id, and rather than
//     rewriting the card (which would burn a revision slot recording no change)
//     this returns the stored card untouched. A caller that forks
//     speculatively — the world doctor accepting a proposal that turned out to
//     be a no-op — gets the original back instead of a duplicate.
//
// The portrait travels, for Duplicate's reason: the stored avatar has already
// been through normalizeAvatar, so the fork inherits the picture the original
// actually shows rather than a second downscale of it.
//
// Fork does NOT record where the fork came from. That is deliberate: provenance
// is terva-owned metadata ABOUT a card, so it lives outside the card directory
// like history and model prefs do (see cardorigin.go). Keeping it out of here
// also means an exported or shared fork carries no trace of the local World it
// was made for.
func (s *CardStore) Fork(id string, cardJSON []byte) (StoredCard, error) {
	if err := slug.ValidID(id); err != nil {
		return StoredCard{}, err
	}
	if _, err := os.Stat(filepath.Join(s.dir, id, cardJSONName)); err != nil {
		if os.IsNotExist(err) {
			return StoredCard{}, fmt.Errorf("card %q not found", id)
		}
		return StoredCard{}, err
	}
	c, err := card.ParseJSON(cardJSON)
	if err != nil {
		return StoredCard{}, err
	}
	raw, err := card.Marshal(c)
	if err != nil {
		return StoredCard{}, err
	}
	// "Did anything change?" is answered against the card's STORED BYTES, never
	// against a re-derived id. A card's id is minted at IMPORT and never
	// re-derived (Edit rewrites the same directory), so after a single edit the
	// id no longer equals the hash of its contents. Comparing derived ids would
	// then call a document identical to what is stored a change, and fork a
	// perfect twin of the card — which is exactly what a caller writing the
	// current document back expects not to happen.
	stored, err := os.ReadFile(filepath.Join(s.dir, id, cardJSONName))
	if err != nil {
		return StoredCard{}, err
	}
	if bytes.Equal(stored, raw) {
		return s.Get(id)
	}
	forked := slug.ID(c.Name, raw)
	if existing, err := os.ReadFile(filepath.Join(s.dir, forked, cardJSONName)); err == nil {
		// A card is already filed here. If it holds byte-for-byte what the fork
		// would write, it IS the fork — hand it back, nothing is displaced.
		if bytes.Equal(existing, raw) {
			return s.Get(forked)
		}
		// Otherwise writing would overwrite a DIFFERENT card, which is a merge
		// wearing a fork's clothes. Reachable when the edit restores a card to
		// contents it once had: those contents hash back to the id they were
		// imported under, and writing them would silently revert the card every
		// other World is reading — the precise failure this method exists to
		// prevent.
		return StoredCard{}, fmt.Errorf("these contents already belong to another card in your library (%s)", forked)
	}
	avatar, _ := os.ReadFile(filepath.Join(s.dir, id, cardAvatarName))
	return s.write(forked, c, raw, avatar, false)
}

// Edit replaces a stored card's fields with a full edited card document
// (CCv2 wrapper or flat), re-serializing it canonically. The id (directory) and
// any avatar are unchanged — editing card data never moves or reshoots the
// portrait. The payload is validated (a card needs a name) before anything is
// written.
func (s *CardStore) Edit(id string, cardJSON []byte) (StoredCard, error) {
	if err := slug.ValidID(id); err != nil {
		return StoredCard{}, err
	}
	if _, err := os.Stat(filepath.Join(s.dir, id, cardJSONName)); err != nil {
		if os.IsNotExist(err) {
			return StoredCard{}, fmt.Errorf("card %q not found", id)
		}
		return StoredCard{}, err
	}
	c, err := card.ParseJSON(cardJSON)
	if err != nil {
		return StoredCard{}, err
	}
	raw, err := card.Marshal(c)
	if err != nil {
		return StoredCard{}, err
	}
	return s.write(id, c, raw, nil, false)
}

// RestoreRevision puts a retained revision back, portrait included. The picture
// travels with the data because a revision may have been recorded by an IMPORT
// that replaced both — restoring only half would leave a character wearing the
// wrong face.
func (s *CardStore) RestoreRevision(id, ref string) (StoredCard, error) {
	if err := slug.ValidID(id); err != nil {
		return StoredCard{}, err
	}
	raw, err := s.history().Get(id, ref)
	if err != nil {
		return StoredCard{}, err
	}
	c, err := card.ParseJSON(raw)
	if err != nil {
		return StoredCard{}, err
	}
	// AvatarAsOf reports "no stored portrait" when the picture has not moved
	// since that revision, in which case the card's current one is already right
	// and write() is told to leave it alone.
	avatar, _, err := s.history().AvatarAsOf(id, ref)
	if err != nil {
		return StoredCard{}, err
	}
	return s.write(id, c, raw, avatar, false)
}

// write is THE choke point every mutation of a stored card funnels through —
// import, edit, restore — which is exactly why the snapshot lives here and not
// in a caller. A path that rewrites fields nobody typed cannot opt out of being
// undoable by forgetting to ask, and an import that lands on a card you already
// have is one of those paths: its id is derived from the card's content, so
// re-importing a file you have since edited resolves to the SAME directory.
//
// avatar nil means "leave the portrait alone" (an edit never reshoots it).
// Non-nil replaces it, and the outgoing picture rides into the same revision as
// the outgoing data. noteReplacement asks for a warning when this write landed
// on a card that was already there — see replacedNote.
func (s *CardStore) write(id string, c card.Card, raw, avatar []byte, noteReplacement bool) (StoredCard, error) {
	dir := filepath.Join(s.dir, id)
	// Missing files read as empty, which is exactly right for a first import:
	// nothing is being replaced, so nothing is recorded.
	prev, _ := os.ReadFile(filepath.Join(dir, cardJSONName))
	prevAvatar, _ := os.ReadFile(filepath.Join(dir, cardAvatarName))
	dataChanged := len(prev) > 0 && !bytes.Equal(prev, raw)
	avatarChanged := len(prevAvatar) > 0 && len(avatar) > 0 && !bytes.Equal(prevAvatar, avatar)

	var warnings []string
	// A write that changes nothing loses nothing, so it must not consume a
	// retention slot — otherwise a few idle saves would push the revisions that
	// matter out of the window.
	if dataChanged || avatarChanged {
		outgoing := []byte(nil)
		if avatarChanged {
			outgoing = prevAvatar
		}
		// Best-effort, and deliberately BEFORE the write: a full disk under
		// card-history must not stop you editing a character, but losing the old
		// revision silently is not acceptable either, so a failure is reported as
		// a warning on a write that otherwise succeeded. Snapshotting first means
		// the worst case is a redundant snapshot (which the next write dedupes
		// away) rather than a window where the old bytes are gone and unrecorded.
		if err := s.history().Snapshot(id, prev, outgoing); err != nil {
			warnings = append(warnings, fmt.Sprintf("could not record the previous revision: %v", err))
		} else if noteReplacement {
			warnings = append(warnings, replacedNote(dataChanged, avatarChanged)...)
		}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return StoredCard{}, err
	}
	if err := os.WriteFile(filepath.Join(dir, cardJSONName), raw, 0o644); err != nil {
		return StoredCard{}, err
	}
	if len(avatar) > 0 {
		if err := os.WriteFile(filepath.Join(dir, cardAvatarName), avatar, 0o644); err != nil {
			return StoredCard{}, err
		}
	}
	return StoredCard{ID: id, Card: c, Raw: raw, AvatarExt: avatarExt(dir), Warnings: warnings}, nil
}

// replacedNote says what a write displaced. An import that lands on a card you
// already have used to be silent, and silence there read as "imported" when
// what happened was "your edits were reverted" — the one outcome a person would
// have wanted to be asked about.
func replacedNote(dataChanged, avatarChanged bool) []string {
	switch {
	case dataChanged && avatarChanged:
		return []string{"this replaced a card already in your library, including its portrait. The version you had is under Earlier versions."}
	case avatarChanged:
		return []string{"this replaced the portrait of a card already in your library. The picture you had is under Earlier versions."}
	case dataChanged:
		return []string{"this replaced a card already in your library. The version you had is under Earlier versions."}
	}
	return nil
}

// Delete removes a card, its avatar, and its revision history from the library.
// History goes with it deliberately: a retained revision is the character's own
// prose, so keeping it after a delete would leave behind exactly what the delete
// was asked to remove.
func (s *CardStore) Delete(id string) error {
	if err := slug.ValidID(id); err != nil {
		return err
	}
	dir := filepath.Join(s.dir, id)
	if _, err := os.Stat(dir); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("card %q not found", id)
		}
		return err
	}
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	if err := s.history().Forget(id); err != nil {
		return fmt.Errorf("card %q was deleted, but its saved revisions could not be removed: %w", id, err)
	}
	return nil
}

// AvatarPath returns the filesystem path of a card's retained avatar image, or
// "" if the card has none or the id is invalid. The /media route serves it.
func (s *CardStore) AvatarPath(id string) string {
	if slug.ValidID(id) != nil {
		return ""
	}
	dir := filepath.Join(s.dir, id)
	if avatarExt(dir) == "" {
		return ""
	}
	return filepath.Join(dir, cardAvatarName)
}

// cardFavoritesName is the flat set of favorited card ids, a sibling of the
// cards/ directory (so it rides TERVA_HOME with the store). Favoriting is a
// per-library preference — a highlight and a sort priority, not card data — so
// it lives OUTSIDE the card: filing one never rewrites the card, and deleting a
// card never touches this file (a stale id is filtered on read, like a group's
// stale member).
const cardFavoritesName = "card-favorites.json"

func (s *CardStore) favoritesPath() string {
	return filepath.Join(filepath.Dir(s.dir), cardFavoritesName)
}

// Favorites returns the set of favorited card ids. A missing file is an empty
// set, never an error.
func (s *CardStore) Favorites() (map[string]bool, error) {
	raw, err := os.ReadFile(s.favoritesPath())
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]bool{}, nil
		}
		return nil, err
	}
	var ids []string
	if err := json.Unmarshal(raw, &ids); err != nil {
		return nil, fmt.Errorf("card favorites: %w", err)
	}
	set := make(map[string]bool, len(ids))
	for _, id := range ids {
		set[id] = true
	}
	return set, nil
}

// SetFavorite adds or removes a card id from the favorites set. Idempotent; the
// id is validated (a favorite is only meaningful for a real card id) and the set
// is written sorted so the file is stable.
func (s *CardStore) SetFavorite(id string, fav bool) error {
	if err := slug.ValidID(id); err != nil {
		return err
	}
	set, err := s.Favorites()
	if err != nil {
		return err
	}
	if set[id] == fav {
		return nil
	}
	if fav {
		set[id] = true
	} else {
		delete(set, id)
	}
	ids := make([]string, 0, len(set))
	for k := range set {
		ids = append(ids, k)
	}
	sort.Strings(ids)
	raw, err := json.Marshal(ids)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.favoritesPath()), 0o755); err != nil {
		return err
	}
	return os.WriteFile(s.favoritesPath(), raw, 0o644)
}

// ResolveCardRef turns a --card / CreateOpts.Card reference into a path
// card.Load can read. An existing readable file passes through unchanged (the
// classic --card <path> flow); otherwise the ref is treated as a library id and
// resolved to that card's stored card.json — which is itself a valid card
// document, so nothing downstream has to know the difference. This is what lets a
// controller spawn a session straight from a library id.
func ResolveCardRef(ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", nil
	}
	if fi, err := os.Stat(ref); err == nil && !fi.IsDir() {
		return ref, nil
	}
	if slug.ValidID(ref) == nil {
		p := filepath.Join(CardsDir(), ref, cardJSONName)
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			return p, nil
		}
	}
	return "", fmt.Errorf("card %q is neither a readable file nor a library id", ref)
}

// avatarExt reports the retained avatar's extension for a card dir, or "".
func avatarExt(dir string) string {
	if _, err := os.Stat(filepath.Join(dir, cardAvatarName)); err == nil {
		return "png"
	}
	return ""
}
